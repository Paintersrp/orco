package probe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"github.com/Paintersrp/orco/internal/stack"
)

type commandProber struct {
	command []string
}

func newCommandProber(spec *stack.CommandProbe) (Prober, error) {
	if len(spec.Command) == 0 {
		return nil, errors.New("probe: command requires at least one argument")
	}
	return &commandProber{command: append([]string(nil), spec.Command...)}, nil
}

func (p *commandProber) Probe(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, p.command[0], p.command[1:]...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("exit %d", exitErr.ExitCode())
		}
		return fmt.Errorf("command failed: %w", err)
	}
	return nil
}
