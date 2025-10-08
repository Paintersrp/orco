package podman

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	nettypes "github.com/containers/common/libnetwork/types"
	"github.com/containers/podman/v4/pkg/bindings"
	podcontainers "github.com/containers/podman/v4/pkg/bindings/containers"
	"github.com/containers/podman/v4/pkg/bindings/system"
	"github.com/containers/podman/v4/pkg/specgen"
	"github.com/docker/docker/volume/mounts"
	"github.com/docker/go-connections/nat"
	ocispec "github.com/opencontainers/runtime-spec/specs-go"

	"github.com/Paintersrp/orco/internal/probe"
	"github.com/Paintersrp/orco/internal/runtime"
	"github.com/Paintersrp/orco/internal/runtime/containerutil"
	"github.com/Paintersrp/orco/internal/stack"
)

type runtimeImpl struct {
	connCtx  context.Context
	connOnce sync.Once
	connErr  error
}

func New() runtime.Runtime {
	return &runtimeImpl{}
}

func init() {
	runtime.Register("podman", New)
}

func (r *runtimeImpl) getConnection() (context.Context, error) {
	r.connOnce.Do(func() {
		ctx, err := bindings.NewConnection(context.Background(), "")
		if err != nil {
			r.connErr = err
			return
		}
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if _, err := system.Version(pingCtx, nil); err != nil {
			r.connErr = err
			return
		}
		r.connCtx = ctx
	})
	return r.connCtx, r.connErr
}

func (r *runtimeImpl) callContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	base, err := r.getConnection()
	if err != nil {
		return nil, nil, fmt.Errorf("create podman connection: %w", err)
	}
	callCtx, cancel := context.WithCancel(base)
	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				cancel()
			case <-callCtx.Done():
			}
		}()
	}
	return callCtx, cancel, nil
}

func (r *runtimeImpl) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Handle, error) {
	if spec.Image == "" {
		return nil, errors.New("service image is required")
	}

	commonSpec, err := containerutil.PrepareCommonSpec(spec)
	if err != nil {
		return nil, err
	}

	if err := r.ensureImage(ctx, spec.Image); err != nil {
		return nil, err
	}

	generator, err := buildSpec(spec, commonSpec)
	if err != nil {
		return nil, err
	}

	createCtx, createCancel, err := r.callContext(ctx)
	if err != nil {
		return nil, err
	}
	createResp, err := podcontainers.CreateWithSpec(createCtx, generator, nil)
	createCancel()
	if err != nil {
		return nil, fmt.Errorf("container create: %w", err)
	}
	containerID := createResp.ID

	startCtx, startCancel, err := r.callContext(ctx)
	if err != nil {
		return nil, err
	}
	err = podcontainers.Start(startCtx, containerID, nil)
	startCancel()
	if err != nil {
		return nil, fmt.Errorf("container start: %w", err)
	}

	var health *stack.Health
	if spec.Health != nil {
		health = spec.Health.Clone()
	} else if spec.Service != nil {
		health = spec.Service.Health.Clone()
	}

	inst := newPodmanInstance(r, containerID, spec.Name, health, commonSpec.MemoryLimit)

	if inst.health == nil {
		inst.signalReady(nil)
		close(inst.healthEvents)
		close(inst.healthDone)
	} else {
		prober, err := probe.New(inst.health)
		if err != nil {
			inst.signalReady(err)
			close(inst.healthEvents)
			close(inst.healthDone)
		} else {
			inst.healthProber = prober
			if observer, ok := prober.(probe.LogObserver); ok {
				inst.logObservers = append(inst.logObservers, observer)
			}
			inst.startHealthMonitor()
		}
	}

	inst.startLogStreamer()
	inst.startWaiter()

	return inst, nil
}

func (r *runtimeImpl) ensureImage(ctx context.Context, image string) error {
	callCtx, cancel, err := r.callContext(ctx)
	if err != nil {
		return err
	}
	defer cancel()

	conn, err := bindings.GetClient(callCtx)
	if err != nil {
		return fmt.Errorf("get podman client: %w", err)
	}

	existsResp, err := conn.DoRequest(callCtx, nil, http.MethodGet, "/images/%s/exists", nil, nil, image)
	if err != nil {
		return fmt.Errorf("inspect image: %w", err)
	}
	if existsResp.IsSuccess() {
		existsResp.Body.Close()
		return nil
	}
	if existsResp.Response.StatusCode != http.StatusNotFound {
		err = existsResp.Process(nil)
		existsResp.Body.Close()
		if err == nil {
			err = fmt.Errorf("unexpected status %d", existsResp.Response.StatusCode)
		}
		return fmt.Errorf("inspect image: %w", err)
	}
	existsResp.Body.Close()

	params := url.Values{}
	params.Set("reference", image)
	params.Set("quiet", "true")

	pullResp, err := conn.DoRequest(callCtx, nil, http.MethodPost, "/images/pull", params, nil)
	if err != nil {
		return fmt.Errorf("pull image: %w", err)
	}
	if !pullResp.IsSuccess() {
		pullErr := pullResp.Process(nil)
		pullResp.Body.Close()
		if pullErr == nil {
			pullErr = fmt.Errorf("unexpected status %d", pullResp.Response.StatusCode)
		}
		return fmt.Errorf("pull image: %w", pullErr)
	}
	_, _ = io.Copy(io.Discard, pullResp.Body)
	pullResp.Body.Close()
	return nil
}

func buildSpec(spec runtime.StartSpec, common containerutil.CommonSpec) (*specgen.SpecGenerator, error) {
	generator := specgen.NewSpecGenerator(spec.Image, false)
	generator.Name = spec.Name

	if len(common.Cmd) > 0 {
		generator.Command = append([]string(nil), common.Cmd...)
	}
	if common.Workdir != "" {
		generator.WorkDir = common.Workdir
	}
	if len(common.Env) > 0 {
		env := make(map[string]string, len(common.Env))
		for _, kv := range common.Env {
			parts := strings.SplitN(kv, "=", 2)
			key := parts[0]
			value := ""
			if len(parts) == 2 {
				value = parts[1]
			}
			env[key] = value
		}
		generator.Env = env
	}

	if len(common.Ports) > 0 {
		ports, err := convertPortMappings(common.Ports)
		if err != nil {
			return nil, err
		}
		generator.PortMappings = ports
	}

	if len(common.Binds) > 0 {
		mounts, err := convertBinds(common.Binds)
		if err != nil {
			return nil, err
		}
		generator.Mounts = mounts
	}

	if common.Resources != (containerutil.Resources{}) {
		res := &ocispec.LinuxResources{}
		if common.Resources.Memory != 0 || common.Resources.MemorySwap != 0 || common.Resources.MemoryReservation != 0 {
			mem := &ocispec.LinuxMemory{}
			if common.Resources.Memory != 0 {
				limit := common.Resources.Memory
				mem.Limit = &limit
			}
			if common.Resources.MemorySwap != 0 {
				swap := common.Resources.MemorySwap
				mem.Swap = &swap
			}
			if common.Resources.MemoryReservation != 0 {
				reservation := common.Resources.MemoryReservation
				mem.Reservation = &reservation
			}
			res.Memory = mem
		}
		if common.Resources.CPUPeriod != 0 || common.Resources.CPUQuota != 0 {
			cpu := &ocispec.LinuxCPU{}
			if common.Resources.CPUPeriod != 0 {
				period := uint64(common.Resources.CPUPeriod)
				cpu.Period = &period
				generator.CPUPeriod = period
			}
			if common.Resources.CPUQuota != 0 {
				quota := common.Resources.CPUQuota
				cpu.Quota = &quota
				generator.CPUQuota = quota
			}
			res.CPU = cpu
		}
		generator.ResourceLimits = res
	}

	return generator, nil
}

func convertPortMappings(mappings []containerutil.PortMapping) ([]nettypes.PortMapping, error) {
	out := make([]nettypes.PortMapping, 0, len(mappings))
	for _, mapping := range mappings {
		start, end, err := mapping.Port.Range()
		if err != nil {
			return nil, fmt.Errorf("parse container port range %q: %w", mapping.Port.Port(), err)
		}
		portRange := end - start + 1
		if portRange < 1 {
			portRange = 1
		}
		proto := mapping.Port.Proto()
		for _, binding := range mapping.Bindings {
			hostStart, hostEnd, err := nat.ParsePortRangeToInt(binding.HostPort)
			if err != nil {
				return nil, fmt.Errorf("parse host port %q: %w", binding.HostPort, err)
			}
			if hostStart == 0 && hostEnd == 0 {
				hostEnd = hostStart
			}
			if hostEnd < hostStart {
				hostEnd = hostStart
			}
			hostRange := hostEnd - hostStart + 1
			if hostRange != portRange && hostStart != 0 {
				return nil, fmt.Errorf("host port range %q does not match container range for %s", binding.HostPort, mapping.Port.Port())
			}
			out = append(out, nettypes.PortMapping{
				ContainerPort: uint16(start),
				HostPort:      uint16(hostStart),
				Range:         uint16(portRange),
				Protocol:      proto,
				HostIP:        binding.HostIP,
			})
		}
	}
	return out, nil
}

func convertBinds(binds []string) ([]ocispec.Mount, error) {
	parser := mounts.NewParser()
	mountsOut := make([]ocispec.Mount, 0, len(binds))
	for _, bindSpec := range binds {
		mp, err := parser.ParseMountRaw(bindSpec, "")
		if err != nil {
			return nil, fmt.Errorf("parse volume %q: %w", bindSpec, err)
		}
		options := collectMountOptions(*mp)
		mountsOut = append(mountsOut, ocispec.Mount{
			Type:        "bind",
			Source:      mp.Source,
			Destination: mp.Destination,
			Options:     options,
		})
	}
	return mountsOut, nil
}

func collectMountOptions(mp mounts.MountPoint) []string {
	opts := make([]string, 0, 4)
	seen := make(map[string]struct{})
	add := func(opt string) {
		opt = strings.TrimSpace(opt)
		if opt == "" {
			return
		}
		if _, ok := seen[opt]; ok {
			return
		}
		seen[opt] = struct{}{}
		opts = append(opts, opt)
	}
	if mp.Spec.ReadOnly {
		add("ro")
	}
	if mp.Mode != "" {
		for _, opt := range strings.Split(mp.Mode, ",") {
			if opt == "ro" && mp.Spec.ReadOnly {
				continue
			}
			if opt == "rw" && !mp.Spec.ReadOnly {
				continue
			}
			add(opt)
		}
	}
	if mp.Spec.BindOptions != nil {
		if mp.Spec.BindOptions.Propagation != "" {
			add(string(mp.Spec.BindOptions.Propagation))
		}
		if mp.Spec.BindOptions.NonRecursive {
			add("rbind")
		}
	}
	return opts
}

type podmanInstance struct {
	runtime      *runtimeImpl
	containerID  string
	name         string
	health       *stack.Health
	healthProber probe.Prober
	memoryLimit  string

	logs         chan runtime.LogEntry
	logCtx       context.Context
	logStop      context.CancelFunc
	logOnce      sync.Once
	logDone      chan struct{}
	logObservers []probe.LogObserver

	healthEvents chan probe.State
	healthCtx    context.Context
	healthStop   context.CancelFunc
	healthDone   chan struct{}

	waitOnce   sync.Once
	waitDone   chan struct{}
	waitResult waitOutcome

	readyOnce sync.Once
	readyDone chan struct{}
	readyErr  error

	stopOnce sync.Once
	stopErr  error
}

type waitOutcome struct {
	exitCode    int64
	err         error
	errorMsg    string
	oomKilled   bool
	memoryLimit string
}

func newPodmanInstance(rt *runtimeImpl, id string, name string, health *stack.Health, memoryLimit string) *podmanInstance {
	logCtx, logCancel := context.WithCancel(context.Background())
	healthCtx, healthCancel := context.WithCancel(context.Background())
	return &podmanInstance{
		runtime:      rt,
		containerID:  id,
		name:         name,
		health:       health,
		memoryLimit:  strings.TrimSpace(memoryLimit),
		logs:         make(chan runtime.LogEntry, 128),
		logCtx:       logCtx,
		logStop:      logCancel,
		logDone:      make(chan struct{}),
		healthEvents: make(chan probe.State, 1),
		healthCtx:    healthCtx,
		healthStop:   healthCancel,
		healthDone:   make(chan struct{}),
		waitDone:     make(chan struct{}),
		readyDone:    make(chan struct{}),
	}
}

func (i *podmanInstance) startLogStreamer() {
	i.logOnce.Do(func() {
		go func() {
			defer close(i.logs)
			defer close(i.logDone)

			stdoutWriter := containerutil.NewLogWriter(i.logCtx, i.deliverLog, runtime.LogSourceStdout, "")
			stderrWriter := containerutil.NewLogWriter(i.logCtx, i.deliverLog, runtime.LogSourceStderr, "warn")

			stdoutCh := make(chan string, 64)
			stderrCh := make(chan string, 64)

			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-i.logCtx.Done():
						return
					case msg, ok := <-stdoutCh:
						if !ok {
							return
						}
						_, _ = stdoutWriter.Write([]byte(msg))
					}
				}
			}()
			go func() {
				defer wg.Done()
				for {
					select {
					case <-i.logCtx.Done():
						return
					case msg, ok := <-stderrCh:
						if !ok {
							return
						}
						_, _ = stderrWriter.Write([]byte(msg))
					}
				}
			}()

			logCtx, cancel, err := i.runtime.callContext(i.logCtx)
			if err != nil {
				cancel()
				close(stdoutCh)
				close(stderrCh)
				wg.Wait()
				stdoutWriter.Close()
				stderrWriter.Close()
				return
			}
			opts := new(podcontainers.LogOptions).
				WithFollow(true).
				WithStdout(true).
				WithStderr(true).
				WithTail("all")
			err = podcontainers.Logs(logCtx, i.containerID, opts, stdoutCh, stderrCh)
			cancel()
			close(stdoutCh)
			close(stderrCh)
			wg.Wait()
			stdoutWriter.Close()
			stderrWriter.Close()
			if err != nil {
				return
			}
		}()
	})
}

func (i *podmanInstance) deliverLog(entry runtime.LogEntry) {
	if entry.Message == "" {
		return
	}
	select {
	case i.logs <- entry:
	case <-i.logCtx.Done():
		return
	}
	i.forwardLog(entry)
}

func (i *podmanInstance) forwardLog(entry runtime.LogEntry) {
	if len(i.logObservers) == 0 {
		return
	}
	logEntry := probe.LogEntry{Message: entry.Message, Source: entry.Source, Level: entry.Level}
	for _, observer := range i.logObservers {
		observer.ObserveLog(logEntry)
	}
}

func (i *podmanInstance) startWaiter() {
	go func() {
		waitCtx, cancel, err := i.runtime.callContext(context.Background())
		if err != nil {
			outcome := waitOutcome{err: err, memoryLimit: i.memoryLimit}
			i.setWaitOutcome(outcome)
			return
		}
		exitCode, waitErr := podcontainers.Wait(waitCtx, i.containerID, nil)
		cancel()
		outcome := waitOutcome{exitCode: int64(exitCode), err: waitErr, memoryLimit: i.memoryLimit}
		i.annotateWaitOutcome(&outcome)
		i.setWaitOutcome(outcome)
	}()
}

func (i *podmanInstance) setWaitOutcome(outcome waitOutcome) {
	i.waitOnce.Do(func() {
		i.waitResult = outcome
		close(i.waitDone)
	})
}

func (i *podmanInstance) annotateWaitOutcome(outcome *waitOutcome) {
	inspectCtx, cancel, err := i.runtime.callContext(context.Background())
	if err != nil {
		return
	}
	defer cancel()
	inspect, err := podcontainers.Inspect(inspectCtx, i.containerID, nil)
	if err != nil {
		return
	}
	if inspect.State != nil {
		if outcome.exitCode == 0 && inspect.State.ExitCode != 0 {
			outcome.exitCode = int64(inspect.State.ExitCode)
		}
		if inspect.State.OOMKilled {
			outcome.oomKilled = true
		}
		if inspect.State.Error != "" {
			outcome.errorMsg = inspect.State.Error
		}
	}
}

func (i *podmanInstance) signalReady(err error) {
	i.readyOnce.Do(func() {
		i.readyErr = err
		close(i.readyDone)
	})
}

func (i *podmanInstance) startHealthMonitor() {
	if i.healthProber == nil {
		return
	}
	go func() {
		defer close(i.healthDone)
		events := probe.Watch(i.healthCtx, i.healthProber, i.health, nil)
		readyReported := false

		for {
			select {
			case <-i.healthCtx.Done():
				if !readyReported {
					readyReported = true
					readyErr := i.healthCtx.Err()
					if readyErr == nil {
						readyErr = context.Canceled
					}
					i.signalReady(readyErr)
				}
				close(i.healthEvents)
				return
			case event, ok := <-events:
				if !ok {
					if !readyReported {
						readyReported = true
						readyErr := i.healthCtx.Err()
						if readyErr == nil {
							readyErr = errors.New("probe ended before reporting readiness")
						}
						i.signalReady(readyErr)
					}
					close(i.healthEvents)
					return
				}

				if !readyReported {
					switch event.Status {
					case probe.StatusReady:
						readyReported = true
						i.signalReady(nil)
					case probe.StatusUnready:
						readyErr := event.Err
						if readyErr == nil && event.Reason != "" {
							readyErr = errors.New(event.Reason)
						}
						if readyErr == nil {
							readyErr = errors.New("probe reported unready before initial readiness")
						}
						readyReported = true
						i.signalReady(readyErr)
					}
				}

				select {
				case i.healthEvents <- event:
				case <-i.healthCtx.Done():
					close(i.healthEvents)
					return
				}
			}
		}
	}()
}

func (i *podmanInstance) WaitReady(ctx context.Context) error {
	if i.health == nil {
		select {
		case <-i.waitDone:
			return containerutil.WaitReadyError(i.waitResult.toStatus())
		default:
		}
		return nil
	}

	for {
		select {
		case <-i.readyDone:
			return i.readyErr
		case <-i.waitDone:
			return containerutil.WaitReadyError(i.waitResult.toStatus())
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (i *podmanInstance) Wait(ctx context.Context) error {
	select {
	case <-i.waitDone:
		return containerutil.WaitExitError(i.waitResult.toStatus())
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (i *podmanInstance) Health() <-chan probe.State {
	if i.health == nil {
		return nil
	}
	return i.healthEvents
}

func (i *podmanInstance) Stop(ctx context.Context) error {
	return i.performStop(ctx, false)
}

func (i *podmanInstance) Kill(ctx context.Context) error {
	return i.performStop(ctx, true)
}

func (i *podmanInstance) performStop(ctx context.Context, force bool) error {
	i.stopOnce.Do(func() {
		defer i.shutdownStreams()
		if force {
			killCtx, cancel, err := i.runtime.callContext(ctx)
			if err != nil {
				i.stopErr = err
				return
			}
			err = podcontainers.Kill(killCtx, i.containerID, new(podcontainers.KillOptions).WithSignal("SIGKILL"))
			cancel()
			if err != nil && !isNotFound(err) {
				i.stopErr = fmt.Errorf("container kill: %w", err)
				return
			}
			select {
			case <-i.waitDone:
				i.stopErr = containerutil.WaitExitError(i.waitResult.toStatus())
			case <-ctx.Done():
				i.stopErr = ctx.Err()
			}
			return
		}

		stopCtx, cancel, err := i.runtime.callContext(ctx)
		if err != nil {
			i.stopErr = err
			return
		}
		timeout := uint(10)
		err = podcontainers.Stop(stopCtx, i.containerID, new(podcontainers.StopOptions).WithTimeout(timeout))
		cancel()
		if err != nil {
			if isNotFound(err) {
				i.stopErr = nil
				return
			}
			killCtx, killCancel, killErr := i.runtime.callContext(ctx)
			if killErr == nil {
				killErr = podcontainers.Kill(killCtx, i.containerID, new(podcontainers.KillOptions).WithSignal("SIGKILL"))
				killCancel()
				if killErr != nil && !isNotFound(killErr) {
					i.stopErr = fmt.Errorf("container stop: %v; kill: %w", err, killErr)
					return
				}
			}
			i.stopErr = err
			return
		}
		select {
		case <-i.waitDone:
			i.stopErr = containerutil.WaitExitError(i.waitResult.toStatus())
		case <-ctx.Done():
			i.stopErr = ctx.Err()
		}
	})
	return i.stopErr
}

func (i *podmanInstance) shutdownStreams() {
	if i.logStop != nil {
		i.logStop()
	}
	if i.healthStop != nil {
		i.healthStop()
	}
	<-i.logDone
	<-i.healthDone
}

func (i *podmanInstance) Logs(ctx context.Context) (<-chan runtime.LogEntry, error) {
	if ctx == nil {
		return i.logs, nil
	}

	out := make(chan runtime.LogEntry, cap(i.logs))
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case entry, ok := <-i.logs:
				if !ok {
					return
				}
				select {
				case out <- entry:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return out, nil
}

func (o waitOutcome) toStatus() containerutil.WaitStatus {
	return containerutil.WaitStatus{
		ExitCode:     o.exitCode,
		Err:          o.err,
		ErrorMessage: o.errorMsg,
		OOMKilled:    o.oomKilled,
		MemoryLimit:  o.memoryLimit,
	}
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	code, checkErr := bindings.CheckResponseCode(err)
	if checkErr != nil {
		return false
	}
	return code == http.StatusNotFound
}
