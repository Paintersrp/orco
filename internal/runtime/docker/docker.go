package docker

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/docker/volume/mounts"
	"github.com/docker/go-connections/nat"

	"github.com/Paintersrp/orco/internal/probe"
	"github.com/Paintersrp/orco/internal/runtime"
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

	if err := ensureImage(ctx, cli, spec.Image); err != nil {
		return nil, err
	}

	containerCfg, hostCfg, err := buildConfigs(spec)
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

	inst := newDockerInstance(cli, containerID, spec.Name, health)
	inst.startLogStreamer()
	inst.startWaiter()
	inst.startHealthMonitor()

	return inst, nil
}

type dockerInstance struct {
	cli         *client.Client
	containerID string
	name        string
	health      *stack.Health

	logs    chan runtime.LogEntry
	logCtx  context.Context
	logStop context.CancelFunc
	logOnce sync.Once
	logDone chan struct{}

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
	status container.WaitResponse
	err    error
}

func newDockerInstance(cli *client.Client, id string, name string, health *stack.Health) *dockerInstance {
	logCtx, logCancel := context.WithCancel(context.Background())
	healthCtx, healthCancel := context.WithCancel(context.Background())
	return &dockerInstance{
		cli:          cli,
		containerID:  id,
		name:         name,
		health:       health,
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

			stdout := newLogWriter(i.logCtx, i.logs, runtime.LogSourceStdout, "")
			stderr := newLogWriter(i.logCtx, i.logs, runtime.LogSourceStderr, "warn")
			_, _ = stdcopy.StdCopy(stdout, stderr, reader)
			stdout.Close()
			stderr.Close()
		}()
	})
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
		i.setWaitOutcome(outcome)
	}()
}

func (i *dockerInstance) setWaitOutcome(outcome waitOutcome) {
	i.waitOnce.Do(func() {
		i.waitResult = outcome
		close(i.waitDone)
	})
}

func (i *dockerInstance) signalReady(err error) {
	i.readyOnce.Do(func() {
		i.readyErr = err
		close(i.readyDone)
	})
}

func (i *dockerInstance) startHealthMonitor() {
	if i.health == nil {
		i.signalReady(nil)
		close(i.healthEvents)
		close(i.healthDone)
		return
	}
	go func() {
		defer close(i.healthDone)
		prober, err := probe.New(i.health)
		if err != nil {
			i.signalReady(err)
			close(i.healthEvents)
			return
		}

		events := probe.Watch(i.healthCtx, prober, i.health, nil)
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
	if outcome.err != nil {
		return outcome.err
	}
	if outcome.status.Error != nil {
		if outcome.status.StatusCode != 0 {
			return fmt.Errorf("container exited with status %d: %s", outcome.status.StatusCode, outcome.status.Error.Message)
		}
		return errors.New(outcome.status.Error.Message)
	}
	if outcome.status.StatusCode != 0 {
		return fmt.Errorf("container exited with status %d", outcome.status.StatusCode)
	}
	return errors.New("container exited before ready")
}

func waitOutcomeExitError(outcome waitOutcome) error {
	if outcome.err != nil {
		return outcome.err
	}
	if outcome.status.Error != nil {
		if outcome.status.StatusCode != 0 {
			return fmt.Errorf("container exited with status %d: %s", outcome.status.StatusCode, outcome.status.Error.Message)
		}
		return errors.New(outcome.status.Error.Message)
	}
	if outcome.status.StatusCode != 0 {
		return fmt.Errorf("container exited with status %d", outcome.status.StatusCode)
	}
	return nil
}

type logWriter struct {
	ctx    context.Context
	ch     chan<- runtime.LogEntry
	source string
	level  string
	buf    bytes.Buffer
	mu     sync.Mutex
}

func newLogWriter(ctx context.Context, ch chan<- runtime.LogEntry, source, level string) *logWriter {
	return &logWriter{ctx: ctx, ch: ch, source: source, level: level}
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	total := len(p)
	reader := bufio.NewReader(bytes.NewReader(p))
	for {
		segment, err := reader.ReadBytes('\n')
		if len(segment) > 0 {
			if segment[len(segment)-1] == '\n' {
				w.buf.Write(segment[:len(segment)-1])
				w.emit(w.buf.String())
				w.buf.Reset()
			} else {
				w.buf.Write(segment)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return total, err
		}
	}
	return total, nil
}

func (w *logWriter) emit(line string) {
	if line == "" {
		return
	}
	select {
	case w.ch <- runtime.LogEntry{Message: line, Source: w.source, Level: w.level}:
	case <-w.ctx.Done():
	}
}

func (w *logWriter) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() == 0 {
		return
	}
	w.emit(w.buf.String())
	w.buf.Reset()
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

func buildConfigs(spec runtime.StartSpec) (*container.Config, *container.HostConfig, error) {
	env := make([]string, 0, len(spec.Env))
	for k, v := range spec.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	sort.Strings(env)

	exposed := nat.PortSet{}
	bindings := nat.PortMap{}
	for _, portSpec := range spec.Ports {
		mappings, err := nat.ParsePortSpec(portSpec)
		if err != nil {
			return nil, nil, fmt.Errorf("parse port %q: %w", portSpec, err)
		}
		for _, mapping := range mappings {
			exposed[mapping.Port] = struct{}{}
			bindings[mapping.Port] = append(bindings[mapping.Port], mapping.Binding)
		}
	}

	cmd := spec.Command
	var cmdSlice []string
	if len(cmd) > 0 {
		cmdSlice = append([]string(nil), cmd...)
	}

	config := &container.Config{
		Image:        spec.Image,
		Env:          env,
		Cmd:          strslice.StrSlice(cmdSlice),
		ExposedPorts: exposed,
	}
	if spec.Workdir != "" {
		config.WorkingDir = spec.Workdir
	}
	host := &container.HostConfig{PortBindings: bindings}
	if len(spec.Volumes) > 0 {
		parser := mounts.NewParser()
		host.Binds = make([]string, 0, len(spec.Volumes))
		for _, volume := range spec.Volumes {
			bindSpec := volume.Source + ":" + volume.Target
			if volume.Mode != "" {
				bindSpec += ":" + volume.Mode
			}
			if _, err := parser.ParseMountRaw(bindSpec, ""); err != nil {
				return nil, nil, fmt.Errorf("parse volume %q: %w", bindSpec, err)
			}
			host.Binds = append(host.Binds, bindSpec)
		}
	}
	return config, host, nil
}
