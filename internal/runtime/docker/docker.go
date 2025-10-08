package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"

	"github.com/Paintersrp/orco/internal/probe"
	"github.com/Paintersrp/orco/internal/runtime"
	"github.com/Paintersrp/orco/internal/runtime/containerutil"
	"github.com/Paintersrp/orco/internal/stack"
)

type runtimeImpl struct {
	client     *client.Client
	clientOnce sync.Once
	clientErr  error
}

// New returns a Docker backed runtime implementation.
func New() runtime.Runtime {
	return &runtimeImpl{}
}

func init() {
	runtime.Register("docker", New)
}

func (r *runtimeImpl) getClient() (*client.Client, error) {
	r.clientOnce.Do(func() {
		cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			r.clientErr = err
			return
		}
		r.client = cli
	})
	return r.client, r.clientErr
}

func (r *runtimeImpl) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Handle, error) {
	cli, err := r.getClient()
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}

	if spec.Image == "" {
		return nil, errors.New("service image is required")
	}

	commonSpec, err := containerutil.PrepareCommonSpec(spec)
	if err != nil {
		return nil, err
	}

	if err := ensureImage(ctx, cli, spec.Image); err != nil {
		return nil, err
	}

	containerCfg, hostCfg, err := buildConfigs(spec, commonSpec)
	if err != nil {
		return nil, err
	}

	createResp, err := cli.ContainerCreate(ctx, containerCfg, hostCfg, &network.NetworkingConfig{}, nil, "")
	if err != nil {
		return nil, fmt.Errorf("container create: %w", err)
	}
	containerID := createResp.ID

	if err := cli.ContainerStart(ctx, containerID, types.ContainerStartOptions{}); err != nil {
		return nil, fmt.Errorf("container start: %w", err)
	}

	var health *stack.Health
	if spec.Health != nil {
		health = spec.Health.Clone()
	} else if spec.Service != nil {
		health = spec.Service.Health.Clone()
	}

	inst := newDockerInstance(cli, containerID, spec.Name, health, commonSpec.MemoryLimit)

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

type dockerInstance struct {
	cli          *client.Client
	containerID  string
	name         string
	health       *stack.Health
	healthProber probe.Prober
	memoryLimit  string

	logs    chan runtime.LogEntry
	logCtx  context.Context
	logStop context.CancelFunc
	logOnce sync.Once
	logDone chan struct{}

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
	status      container.WaitResponse
	err         error
	oomKilled   bool
	memoryLimit string
}

func newDockerInstance(cli *client.Client, id string, name string, health *stack.Health, memoryLimit string) *dockerInstance {
	logCtx, logCancel := context.WithCancel(context.Background())
	healthCtx, healthCancel := context.WithCancel(context.Background())
	return &dockerInstance{
		cli:          cli,
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

func (i *dockerInstance) startLogStreamer() {
	i.logOnce.Do(func() {
		go func() {
			defer close(i.logs)
			defer close(i.logDone)
			reader, err := i.cli.ContainerLogs(i.logCtx, i.containerID, types.ContainerLogsOptions{
				ShowStdout: true,
				ShowStderr: true,
				Follow:     true,
				Tail:       "all",
			})
			if err != nil {
				return
			}
			defer reader.Close()

			stdout := containerutil.NewLogWriter(i.logCtx, i.deliverLog, runtime.LogSourceStdout, "")
			stderr := containerutil.NewLogWriter(i.logCtx, i.deliverLog, runtime.LogSourceStderr, "warn")
			_, _ = stdcopy.StdCopy(stdout, stderr, reader)
			stdout.Close()
			stderr.Close()
		}()
	})
}

func (i *dockerInstance) deliverLog(entry runtime.LogEntry) {
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

func (i *dockerInstance) forwardLog(entry runtime.LogEntry) {
	if len(i.logObservers) == 0 {
		return
	}
	logEntry := probe.LogEntry{Message: entry.Message, Source: entry.Source, Level: entry.Level}
	for _, observer := range i.logObservers {
		observer.ObserveLog(logEntry)
	}
}

func (i *dockerInstance) startWaiter() {
	go func() {
		statusCh, errCh := i.cli.ContainerWait(context.Background(), i.containerID, container.WaitConditionNextExit)
		var outcome waitOutcome
		for statusCh != nil || errCh != nil {
			select {
			case err, ok := <-errCh:
				if !ok {
					errCh = nil
					continue
				}
				if err != nil {
					outcome.err = err
					statusCh = nil
					errCh = nil
				}
			case resp, ok := <-statusCh:
				if !ok {
					statusCh = nil
					continue
				}
				outcome.status = resp
				statusCh = nil
				errCh = nil
			}
		}
		i.annotateWaitOutcome(&outcome)
		i.setWaitOutcome(outcome)
	}()
}

func (i *dockerInstance) setWaitOutcome(outcome waitOutcome) {
	i.waitOnce.Do(func() {
		i.waitResult = outcome
		close(i.waitDone)
	})
}

func (i *dockerInstance) annotateWaitOutcome(outcome *waitOutcome) {
	outcome.memoryLimit = i.memoryLimit
	inspect, err := i.cli.ContainerInspect(context.Background(), i.containerID)
	if err != nil {
		return
	}

	state := inspect.State
	if state != nil {
		if outcome.status.StatusCode == 0 && state.ExitCode != 0 {
			outcome.status.StatusCode = int64(state.ExitCode)
		}
		if state.OOMKilled {
			outcome.oomKilled = true
		}
	}
	if outcome.status.StatusCode == 137 {
		outcome.oomKilled = true
	}
}

func (i *dockerInstance) signalReady(err error) {
	i.readyOnce.Do(func() {
		i.readyErr = err
		close(i.readyDone)
	})
}

func (i *dockerInstance) startHealthMonitor() {
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

func (i *dockerInstance) WaitReady(ctx context.Context) error {
	if i.health == nil {
		select {
		case <-i.waitDone:
			return waitOutcomeError(i.waitResult)
		default:
		}
		return nil
	}

	for {
		select {
		case <-i.readyDone:
			return i.readyErr
		case <-i.waitDone:
			return waitOutcomeError(i.waitResult)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (i *dockerInstance) Wait(ctx context.Context) error {
	select {
	case <-i.waitDone:
		return waitOutcomeExitError(i.waitResult)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (i *dockerInstance) Health() <-chan probe.State {
	if i.health == nil {
		return nil
	}
	return i.healthEvents
}

func (i *dockerInstance) Stop(ctx context.Context) error {
	return i.performStop(ctx, false)
}

func (i *dockerInstance) Kill(ctx context.Context) error {
	return i.performStop(ctx, true)
}

func (i *dockerInstance) performStop(ctx context.Context, force bool) error {
	i.stopOnce.Do(func() {
		defer i.shutdownStreams()
		if force {
			err := i.cli.ContainerKill(ctx, i.containerID, "SIGKILL")
			if err != nil && !client.IsErrNotFound(err) {
				i.stopErr = fmt.Errorf("container kill: %w", err)
				return
			}
			select {
			case <-i.waitDone:
				i.stopErr = waitOutcomeExitError(i.waitResult)
			case <-ctx.Done():
				i.stopErr = ctx.Err()
			}
			return
		}

		sec := int((10 * time.Second).Seconds())
		opts := container.StopOptions{Timeout: &sec}
		err := i.cli.ContainerStop(ctx, i.containerID, opts)
		if err != nil {
			if client.IsErrNotFound(err) {
				i.stopErr = nil
				return
			}
			killErr := i.cli.ContainerKill(ctx, i.containerID, "SIGKILL")
			if killErr != nil && !client.IsErrNotFound(killErr) {
				i.stopErr = fmt.Errorf("container stop: %v; kill: %w", err, killErr)
				return
			}
			i.stopErr = err
			return
		}
		select {
		case <-i.waitDone:
			i.stopErr = waitOutcomeExitError(i.waitResult)
		case <-ctx.Done():
			i.stopErr = ctx.Err()
		}
	})
	return i.stopErr
}

func (i *dockerInstance) shutdownStreams() {
	if i.logStop != nil {
		i.logStop()
	}
	if i.healthStop != nil {
		i.healthStop()
	}
	<-i.logDone
	<-i.healthDone
}

func (i *dockerInstance) Logs(ctx context.Context) (<-chan runtime.LogEntry, error) {
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

func waitOutcomeError(outcome waitOutcome) error {
	status := containerutil.WaitStatus{
		ExitCode:    outcome.status.StatusCode,
		Err:         outcome.err,
		OOMKilled:   outcome.oomKilled,
		MemoryLimit: outcome.memoryLimit,
	}
	if outcome.status.Error != nil {
		status.ErrorMessage = outcome.status.Error.Message
	}
	return containerutil.WaitReadyError(status)
}

func waitOutcomeExitError(outcome waitOutcome) error {
	status := containerutil.WaitStatus{
		ExitCode:    outcome.status.StatusCode,
		Err:         outcome.err,
		OOMKilled:   outcome.oomKilled,
		MemoryLimit: outcome.memoryLimit,
	}
	if outcome.status.Error != nil {
		status.ErrorMessage = outcome.status.Error.Message
	}
	return containerutil.WaitExitError(status)
}

func ensureImage(ctx context.Context, cli *client.Client, imageName string) error {
	_, _, err := cli.ImageInspectWithRaw(ctx, imageName)
	if err == nil {
		return nil
	}
	if !client.IsErrNotFound(err) {
		return fmt.Errorf("inspect image: %w", err)
	}
	reader, err := cli.ImagePull(ctx, imageName, types.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull image: %w", err)
	}
	defer reader.Close()
	_, _ = io.Copy(io.Discard, reader)
	return nil
}

func buildConfigs(spec runtime.StartSpec, common containerutil.CommonSpec) (*container.Config, *container.HostConfig, error) {
	exposed := nat.PortSet{}
	bindings := nat.PortMap{}
	for _, mapping := range common.Ports {
		exposed[mapping.Port] = struct{}{}
		copied := append([]nat.PortBinding(nil), mapping.Bindings...)
		bindings[mapping.Port] = append(bindings[mapping.Port], copied...)
	}

	config := &container.Config{
		Image:        spec.Image,
		Env:          append([]string(nil), common.Env...),
		Cmd:          strslice.StrSlice(append([]string(nil), common.Cmd...)),
		ExposedPorts: exposed,
	}
	if common.Workdir != "" {
		config.WorkingDir = common.Workdir
	}

	host := &container.HostConfig{PortBindings: bindings}
	if len(common.Binds) > 0 {
		host.Binds = append([]string(nil), common.Binds...)
	}

	if common.Resources != (containerutil.Resources{}) {
		host.Resources = container.Resources{
			NanoCPUs:          common.Resources.NanoCPUs,
			CPUPeriod:         common.Resources.CPUPeriod,
			CPUQuota:          common.Resources.CPUQuota,
			Memory:            common.Resources.Memory,
			MemorySwap:        common.Resources.MemorySwap,
			MemoryReservation: common.Resources.MemoryReservation,
		}
	}

	return config, host, nil
}
