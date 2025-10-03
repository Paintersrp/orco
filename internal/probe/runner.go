package probe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"slices"
	"time"

	"github.com/example/orco/internal/stack"
)

// Status captures the readiness condition surfaced by a probe runner.
type Status string

const (
	// StatusUnknown is used internally to track transitions and is not
	// emitted on the public channel.
	StatusUnknown Status = "unknown"
	// StatusReady indicates that the probe has satisfied the configured
	// success threshold.
	StatusReady Status = "ready"
	// StatusUnready indicates that the probe has exceeded the configured
	// failure threshold.
	StatusUnready Status = "unready"
)

// State describes a readiness state transition emitted by Watch.
type State struct {
	Status Status
	Err    error
}

// Runner executes readiness probes defined by a stack.Health specification.
type Runner struct {
	health     *stack.Health
	httpClient *http.Client
	dialer     func(ctx context.Context, network, address string) (net.Conn, error)
}

// NewRunner constructs a runner for the provided health specification.
func NewRunner(health *stack.Health) *Runner {
	client := &http.Client{}
	dialer := (&net.Dialer{}).DialContext
	return &Runner{
		health:     health,
		httpClient: client,
		dialer:     dialer,
	}
}

// Run blocks until the probe succeeds according to the configured thresholds or
// returns an error once the failure threshold is exceeded.
func (r *Runner) Run(ctx context.Context) error {
	if r == nil || r.health == nil {
		return nil
	}

	h := r.health
	successNeeded := h.SuccessThreshold
	if successNeeded <= 0 {
		successNeeded = 1
	}
	failureAllowed := h.FailureThreshold
	if failureAllowed <= 0 {
		failureAllowed = 1
	}
	interval := h.Interval.Duration

	if gp := h.GracePeriod.Duration; gp > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(gp):
		}
	}

	successes := 0
	failures := 0
	var lastErr error

	for {
		attemptCtx := ctx
		cancel := func() {}
		if pt := r.probeTimeout(); pt > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, pt)
		}

		err := r.execute(attemptCtx)
		cancel()

		if err == nil {
			successes++
			failures = 0
			if successes >= successNeeded {
				return nil
			}
		} else {
			if ctx.Err() != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return ctx.Err()
				}
			}
			successes = 0
			failures++
			lastErr = err
			if failures >= failureAllowed {
				return fmt.Errorf("probe failed after %d consecutive errors: %w", failures, lastErr)
			}
		}

		if interval <= 0 {
			continue
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// Watch continuously executes the configured probe until the provided context
// is cancelled. State transitions are emitted onto the supplied channel each
// time the probe moves between Ready and Unready conditions. The method blocks
// until the context is cancelled.
func (r *Runner) Watch(ctx context.Context, events chan<- State) {
	if ctx == nil {
		return
	}
	if r == nil || r.health == nil {
		sendState(ctx, events, State{Status: StatusReady})
		<-ctx.Done()
		return
	}

	h := r.health
	successNeeded := h.SuccessThreshold
	if successNeeded <= 0 {
		successNeeded = 1
	}
	failureAllowed := h.FailureThreshold
	if failureAllowed <= 0 {
		failureAllowed = 1
	}
	interval := h.Interval.Duration

	if gp := h.GracePeriod.Duration; gp > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(gp):
		}
	}

	successes := 0
	failures := 0
	var lastErr error
	status := StatusUnknown

	for {
		attemptCtx := ctx
		cancel := func() {}
		if pt := r.probeTimeout(); pt > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, pt)
		}

		err := r.execute(attemptCtx)
		cancel()

		if err == nil {
			successes++
			failures = 0
			if successes >= successNeeded && status != StatusReady {
				if !sendState(ctx, events, State{Status: StatusReady}) {
					return
				}
				status = StatusReady
			}
		} else {
			if ctx.Err() != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return
				}
			}
			successes = 0
			failures++
			lastErr = err
			if failures >= failureAllowed && status != StatusUnready {
				eventErr := fmt.Errorf("probe failed after %d consecutive errors: %w", failures, lastErr)
				if !sendState(ctx, events, State{Status: StatusUnready, Err: eventErr}) {
					return
				}
				status = StatusUnready
			}
		}

		if interval <= 0 {
			select {
			case <-ctx.Done():
				return
			default:
			}
			continue
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func (r *Runner) execute(ctx context.Context) error {
	switch {
	case r.health.HTTP != nil:
		return r.runHTTP(ctx, r.health.HTTP)
	case r.health.TCP != nil:
		return r.runTCP(ctx, r.health.TCP)
	case r.health.Command != nil:
		return r.runCommand(ctx, r.health.Command)
	default:
		return errors.New("no probe configured")
	}
}

func (r *Runner) probeTimeout() time.Duration {
	if r.health == nil {
		return 0
	}
	if r.health.Command != nil && r.health.Command.Timeout.Duration > 0 {
		return r.health.Command.Timeout.Duration
	}
	return r.health.Timeout.Duration
}

func (r *Runner) runHTTP(ctx context.Context, probe *stack.HTTPProbe) error {
	if probe == nil {
		return errors.New("http probe missing configuration")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probe.URL, nil)
	if err != nil {
		return fmt.Errorf("http probe request: %w", err)
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http probe request: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if len(probe.ExpectStatus) > 0 {
		if !slices.Contains(probe.ExpectStatus, resp.StatusCode) {
			return fmt.Errorf("http probe unexpected status %d", resp.StatusCode)
		}
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Errorf("http probe unexpected status %d", resp.StatusCode)
	}
	return nil
}

func (r *Runner) runTCP(ctx context.Context, probe *stack.TCPProbe) error {
	if probe == nil {
		return errors.New("tcp probe missing configuration")
	}
	conn, err := r.dialer(ctx, "tcp", probe.Address)
	if err != nil {
		return fmt.Errorf("tcp probe dial %s: %w", probe.Address, err)
	}
	conn.Close()
	return nil
}

func (r *Runner) runCommand(ctx context.Context, probe *stack.CommandProbe) error {
	if probe == nil {
		return errors.New("command probe missing configuration")
	}
	if len(probe.Command) == 0 {
		return errors.New("command probe requires a command")
	}
	cmd := exec.CommandContext(ctx, probe.Command[0], probe.Command[1:]...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("command probe failed: %w", err)
	}
	return nil
}

func sendState(ctx context.Context, events chan<- State, state State) bool {
	if events == nil {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case events <- state:
		return true
	}
}
