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
	"github.com/docker/go-connections/nat"

	"github.com/example/orco/internal/probe"
	"github.com/example/orco/internal/runtime"
	"github.com/example/orco/internal/stack"
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

func (r *runtimeImpl) Start(ctx context.Context, name string, svc *stack.Service) (runtime.Instance, error) {
	if svc == nil {
		return nil, errors.New("service definition is required")
	}

	cli, err := r.getClient()
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}

	service := svc.Clone()
	if service.Image == "" {
		return nil, errors.New("service image is required")
	}

	if err := ensureImage(ctx, cli, service.Image); err != nil {
		return nil, err
	}

	containerCfg, hostCfg, err := buildConfigs(service)
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

	inst := newDockerInstance(cli, containerID, service)
	inst.startLogStreamer()
	inst.startWaiter()
	inst.startHealthMonitor()

	return inst, nil
}

type dockerInstance struct {
	cli         *client.Client
	containerID string
	svc         *stack.Service

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

func newDockerInstance(cli *client.Client, id string, svc *stack.Service) *dockerInstance {
	logCtx, logCancel := context.WithCancel(context.Background())
	healthCtx, healthCancel := context.WithCancel(context.Background())
	return &dockerInstance{
		cli:          cli,
		containerID:  id,
		svc:          svc,
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
		select {
		case err := <-errCh:
			if err != nil {
				outcome.err = err
			}
		case resp := <-statusCh:
			outcome.status = resp
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
	if i.svc == nil || i.svc.Health == nil {
		i.signalReady(nil)
		close(i.healthEvents)
		close(i.healthDone)
		return
	}
	go func() {
		defer close(i.healthDone)
		runner := probe.NewRunner(i.svc.Health)
		readyCh := make(chan error, 1)
		go func() {
			defer close(readyCh)
			readyCh <- runner.Run(i.healthCtx)
		}()
		go func() {
			defer close(i.healthEvents)
			runner.Watch(i.healthCtx, i.healthEvents)
		}()
		if err := <-readyCh; err != nil {
			i.signalReady(err)
		} else {
			i.signalReady(nil)
		}
		<-i.healthCtx.Done()
	}()
}

func (i *dockerInstance) WaitReady(ctx context.Context) error {
	if i.svc == nil || i.svc.Health == nil {
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

func (i *dockerInstance) Health() <-chan probe.State {
	if i.svc == nil || i.svc.Health == nil {
		return nil
	}
	return i.healthEvents
}

func (i *dockerInstance) Stop(ctx context.Context) error {
	i.stopOnce.Do(func() {
		defer i.shutdownStreams()
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
		i.stopErr = nil
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

func (i *dockerInstance) Logs() <-chan runtime.LogEntry {
	return i.logs
}

func waitOutcomeError(outcome waitOutcome) error {
	if outcome.err != nil {
		return outcome.err
	}
	if outcome.status.StatusCode != 0 {
		return fmt.Errorf("container exited with status %d", outcome.status.StatusCode)
	}
	if outcome.status.Error != nil {
		return errors.New(outcome.status.Error.Message)
	}
	return errors.New("container exited before ready")
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

func buildConfigs(svc *stack.Service) (*container.Config, *container.HostConfig, error) {
	env := make([]string, 0, len(svc.Env))
	for k, v := range svc.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	sort.Strings(env)

	exposed := nat.PortSet{}
	bindings := nat.PortMap{}
	for _, spec := range svc.Ports {
		mappings, err := nat.ParsePortSpec(spec)
		if err != nil {
			return nil, nil, fmt.Errorf("parse port %q: %w", spec, err)
		}
		for _, mapping := range mappings {
			exposed[mapping.Port] = struct{}{}
			bindings[mapping.Port] = append(bindings[mapping.Port], mapping.Binding)
		}
	}

	cmd := svc.Command
	var cmdSlice []string
	if len(cmd) > 0 {
		cmdSlice = append([]string(nil), cmd...)
	}

	config := &container.Config{
		Image:        svc.Image,
		Env:          env,
		Cmd:          strslice.StrSlice(cmdSlice),
		ExposedPorts: exposed,
	}
	host := &container.HostConfig{PortBindings: bindings}
	return config, host, nil
}
