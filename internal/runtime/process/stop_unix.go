//go:build !windows

package process

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"
)

func (p *processInstance) Stop(ctx context.Context) error {
	return p.terminate(ctx, false)
}

func (p *processInstance) Kill(ctx context.Context) error {
	return p.terminate(ctx, true)
}

func (p *processInstance) terminate(ctx context.Context, force bool) error {
	p.cancelWatch()
	if p.cmd.Process == nil {
		return nil
	}

	if !force {
		// Attempt a graceful shutdown first.
		if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("signal process group %s: %w", p.name, err)
		}

		select {
		case <-p.waitDone:
			return p.exitError()
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill process group %s: %w", p.name, err)
	}
	select {
	case <-p.waitDone:
		return p.exitError()
	case <-ctx.Done():
		return ctx.Err()
	}
}
