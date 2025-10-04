package process

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/example/orco/internal/probe"
	"github.com/example/orco/internal/runtime"
	"github.com/example/orco/internal/stack"
)

type runtimeImpl struct{}

// New constructs a runtime that executes services as local processes.
func New() runtime.Runtime {
	return &runtimeImpl{}
}

func (r *runtimeImpl) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Handle, error) {
	if len(spec.Command) == 0 {
		return nil, fmt.Errorf("process runtime for service %s requires a command", spec.Name)
	}

	cmd := exec.CommandContext(ctx, spec.Command[0], spec.Command[1:]...)
	if spec.Workdir != "" {
		cmd.Dir = spec.Workdir
	}

	env := os.Environ()
	if len(spec.Env) > 0 {
		envOverrides := make([]string, 0, len(spec.Env))
		for k, v := range spec.Env {
			envOverrides = append(envOverrides, fmt.Sprintf("%s=%s", k, v))
		}
		env = append(env, envOverrides...)
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("service %s stdout: %w", spec.Name, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("service %s stderr: %w", spec.Name, err)
	}

	configureCmdSysProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start service %s: %w", spec.Name, err)
	}

	var health *stack.Health
	if spec.Health != nil {
		health = spec.Health.Clone()
	} else if spec.Service != nil {
		health = spec.Service.Health.Clone()
	}

	inst := &processInstance{
		name:     spec.Name,
		cmd:      cmd,
		logs:     make(chan runtime.LogEntry, 64),
		waitErr:  make(chan error, 1),
		waitDone: make(chan struct{}),
		health:   health,
	}

	if inst.health != nil {
		prober, err := probe.New(inst.health)
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, fmt.Errorf("create probe: %w", err)
		}

		inst.healthCh = make(chan probe.State, 1)
		inst.readyCh = make(chan struct{})
		inst.readyErr = make(chan error, 1)
		inst.watchCtx, inst.watchCancel = context.WithCancel(context.Background())

		events := probe.Watch(inst.watchCtx, prober, inst.health, nil)
		go inst.observeHealth(events)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go inst.streamLogs(stdout, runtime.LogSourceStdout, &wg)
	go inst.streamLogs(stderr, runtime.LogSourceStderr, &wg)
	go func() {
		wg.Wait()
		close(inst.logs)
	}()

	go func() {
		inst.waitErr <- cmd.Wait()
		close(inst.waitErr)
	}()
	go inst.observeExit()

	return inst, nil
}

type processInstance struct {
	name     string
	cmd      *exec.Cmd
	logs     chan runtime.LogEntry
	waitErr  chan error
	waitDone chan struct{}
	waitOnce sync.Once
	waitRes  error
	health   *stack.Health

	watchCtx    context.Context
	watchCancel context.CancelFunc

	healthCh chan probe.State

	readyCh      chan struct{}
	readyErr     chan error
	readyOnce    sync.Once
	readyErrOnce sync.Once
	initialReady atomic.Bool
}

func (p *processInstance) WaitReady(ctx context.Context) error {
	if p.health == nil || p.readyCh == nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.waitDone:
			return p.wrapExitError()
		default:
			return nil
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-p.readyErr:
			if err == nil {
				return errors.New("probe reported unready before initial readiness")
			}
			return err
		case <-p.readyCh:
			return nil
		case <-p.waitDone:
			return p.wrapExitError()
		}
	}
}

func (p *processInstance) Wait(ctx context.Context) error {
	select {
	case <-p.waitDone:
		return p.wrapExitError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *processInstance) Health() <-chan probe.State {
	return p.healthCh
}

func (p *processInstance) Logs(ctx context.Context) (<-chan runtime.LogEntry, error) {
	if ctx == nil {
		return p.logs, nil
	}

	out := make(chan runtime.LogEntry, cap(p.logs))
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case entry, ok := <-p.logs:
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

func (p *processInstance) streamLogs(r io.Reader, source string, wg *sync.WaitGroup) {
	defer wg.Done()
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\n")
		entry := runtime.LogEntry{Message: line, Source: source}
		if source == runtime.LogSourceStderr {
			entry.Level = "warn"
		}
		p.logs <- entry
	}
}

func (p *processInstance) observeHealth(states <-chan probe.State) {
	defer close(p.healthCh)
	for {
		select {
		case <-p.watchCtx.Done():
			return
		case state, ok := <-states:
			if !ok {
				return
			}
			if state.Status == probe.StatusReady {
				if p.initialReady.CompareAndSwap(false, true) {
					p.readyOnce.Do(func() { close(p.readyCh) })
				}
			} else if state.Status == probe.StatusUnready {
				if !p.initialReady.Load() {
					err := state.Err
					if err == nil {
						err = errors.New("probe reported unready before initial readiness")
					}
					p.readyErrOnce.Do(func() {
						select {
						case p.readyErr <- err:
						default:
						}
					})
				}
			}

			select {
			case p.healthCh <- state:
			case <-p.watchCtx.Done():
				return
			}
		}
	}
}

func (p *processInstance) cancelWatch() {
	if p.watchCancel != nil {
		p.watchCancel()
		p.watchCancel = nil
	}
}

func (p *processInstance) wrapExitError() error {
	err := p.exitError()
	if err == nil {
		return nil
	}
	return fmt.Errorf("process %s exited: %w", p.name, err)
}

func (p *processInstance) exitError() error {
	return p.waitRes
}

func (p *processInstance) recordExit(err error) {
	p.waitOnce.Do(func() {
		p.waitRes = err
		close(p.waitDone)
	})
}

func (p *processInstance) observeExit() {
	err, ok := <-p.waitErr
	if !ok {
		p.recordExit(errors.New("process wait channel closed unexpectedly"))
		return
	}
	p.recordExit(err)
}
