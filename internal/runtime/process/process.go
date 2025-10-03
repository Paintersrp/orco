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
	if svc.Env != nil {
		env := make([]string, 0, len(svc.Env))
		for k, v := range svc.Env {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
		cmd.Env = append(cmd.Env, env...)
	}

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
		logs:    make(chan string, 64),
		waitErr: make(chan error, 1),
		health:  svc.Health.Clone(),
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go inst.streamLogs(stdout, &wg)
	go inst.streamLogs(stderr, &wg)
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
	logs    chan string
	waitErr chan error
	health  *stack.Health
}

func (p *processInstance) WaitReady(ctx context.Context) error {
	if p.health == nil {
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

	runner := probe.NewRunner(p.health)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- runner.Run(runCtx)
	}()

	for {
		select {
		case <-ctx.Done():
			cancel()
			if err := <-resultCh; err != nil && !errors.Is(err, context.Canceled) {
				// Swallow probe errors when the caller cancelled.
			}
			return ctx.Err()
		case err := <-resultCh:
			return err
		case err, ok := <-p.waitErr:
			cancel()
			if probeErr := <-resultCh; probeErr != nil && !errors.Is(probeErr, context.Canceled) {
				// Prefer process exit details below.
			}
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

func (p *processInstance) Stop(ctx context.Context) error {
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

func (p *processInstance) Logs() <-chan string {
	return p.logs
}

func (p *processInstance) streamLogs(r io.Reader, wg *sync.WaitGroup) {
	defer wg.Done()
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\n")
		p.logs <- line
	}
}
