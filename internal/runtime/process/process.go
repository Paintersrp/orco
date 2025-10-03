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
	"syscall"
	"time"

	"github.com/example/orco/internal/probe"
	"github.com/example/orco/internal/runtime"
	"github.com/example/orco/internal/stack"
)

type runtimeImpl struct{}

// New constructs a runtime that executes services as local processes.
func New() runtime.Runtime {
	return &runtimeImpl{}
}

func (r *runtimeImpl) Start(ctx context.Context, name string, svc *stack.Service) (runtime.Instance, error) {
	if len(svc.Command) == 0 {
		return nil, fmt.Errorf("process runtime for service %s requires a command", name)
	}

	cmd := exec.CommandContext(ctx, svc.Command[0], svc.Command[1:]...)
	if svc.ResolvedWorkdir != "" {
		cmd.Dir = svc.ResolvedWorkdir
	}

	env := os.Environ()
	if svc.Env != nil {
		envOverrides := make([]string, 0, len(svc.Env))
		for k, v := range svc.Env {
			envOverrides = append(envOverrides, fmt.Sprintf("%s=%s", k, v))
		}
		env = append(env, envOverrides...)
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("service %s stdout: %w", name, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("service %s stderr: %w", name, err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start service %s: %w", name, err)
	}

	inst := &processInstance{
		name:    name,
		cmd:     cmd,
		logs:    make(chan runtime.LogEntry, 64),
		waitErr: make(chan error, 1),
		health:  svc.Health.Clone(),
	}

	if inst.health != nil {
		inst.healthCh = make(chan probe.State, 1)
		inst.readyCh = make(chan struct{})
		inst.readyErr = make(chan error, 1)
		inst.watchCtx, inst.watchCancel = context.WithCancel(context.Background())

		stateCh := make(chan probe.State, 1)
		go func() {
			defer close(stateCh)
			probe.NewRunner(inst.health).Watch(inst.watchCtx, stateCh)
		}()
		go inst.observeHealth(stateCh)
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

	return inst, nil
}

type processInstance struct {
	name    string
	cmd     *exec.Cmd
	logs    chan runtime.LogEntry
	waitErr chan error
	health  *stack.Health

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
		case err, ok := <-p.waitErr:
			if ok && err != nil {
				return fmt.Errorf("process %s exited: %w", p.name, err)
			}
			if !ok {
				return errors.New("process wait channel closed unexpectedly")
			}
			return nil
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
		case err, ok := <-p.waitErr:
			if !ok {
				return errors.New("process wait channel closed unexpectedly")
			}
			if err != nil {
				return fmt.Errorf("process %s exited: %w", p.name, err)
			}
			return nil
		}
	}
}

func (p *processInstance) Health() <-chan probe.State {
	return p.healthCh
}

func (p *processInstance) Stop(ctx context.Context) error {
	p.cancelWatch()
	if p.cmd.Process == nil {
		return nil
	}
	// Attempt a graceful shutdown first.
	_ = p.cmd.Process.Signal(syscall.SIGTERM)

	select {
	case err, ok := <-p.waitErr:
		if ok {
			return err
		}
		return nil
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
		return ctx.Err()
	}

	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("kill process %s: %w", p.name, err)
	}
	if err, ok := <-p.waitErr; ok {
		return err
	}
	return nil
}

func (p *processInstance) Logs() <-chan runtime.LogEntry {
	return p.logs
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
