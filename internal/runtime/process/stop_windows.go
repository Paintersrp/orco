//go:build windows

package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

func (p *processInstance) Stop(ctx context.Context) error {
	p.cancelWatch()
	if p.cmd.Process == nil {
		return nil
	}
	// Attempt a graceful shutdown first.
	_ = p.cmd.Process.Signal(os.Interrupt)

	select {
	case <-p.waitDone:
		return p.exitError()
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
		return ctx.Err()
	}

	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("kill process %s: %w", p.name, err)
	}
	select {
	case <-p.waitDone:
		return p.exitError()
	case <-ctx.Done():
		return ctx.Err()
	}
}
