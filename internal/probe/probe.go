package probe

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/example/orco/internal/stack"
)

// Status captures the readiness condition surfaced by a probe watcher.
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

// Event describes a readiness state transition emitted by Watch.
type Event struct {
	Status Status
	Reason string
	Err    error
	At     time.Time
}

// State remains available for callers that still rely on the previous naming
// while allowing the new API to return Event values.
type State = Event

// Prober defines the behaviour required by the Watch loop.
type Prober interface {
	Probe(ctx context.Context) error
}

// New constructs an implementation of Prober for the supplied specification.
func New(spec *stack.Health) (Prober, error) {
	if spec == nil {
		return nil, nil
	}
	switch {
	case spec.HTTP != nil:
		return newHTTPProber(spec.HTTP), nil
	case spec.TCP != nil:
		return newTCPProber(spec.TCP), nil
	case spec.Command != nil:
		return newCommandProber(spec.Command)
	default:
		return nil, errors.New("probe: missing configuration")
	}
}

// Watch continuously executes the provided prober until the context is
// cancelled. Transitions between ready and unready states are emitted on the
// returned channel. The channel is closed once the context is cancelled.
func Watch(ctx context.Context, prober Prober, spec *stack.Health, nowFn func() time.Time) <-chan Event {
	events := make(chan Event, 1)
	if ctx == nil {
		close(events)
		return events
	}
	if nowFn == nil {
		nowFn = time.Now
	}
	go func() {
		defer close(events)
		if prober == nil || spec == nil {
			return
		}

		successNeeded := spec.SuccessThreshold
		if successNeeded <= 0 {
			successNeeded = 1
		}
		failureAllowed := spec.FailureThreshold
		if failureAllowed <= 0 {
			failureAllowed = 1
		}

		interval := spec.Interval.Duration
		timeout := probeTimeout(spec)

		if gp := spec.GracePeriod.Duration; gp > 0 {
			timer := time.NewTimer(gp)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
		}

		successes := 0
		failures := 0
		status := StatusUnknown

		for {
			attemptCtx := ctx
			cancel := func() {}
			if timeout > 0 {
				attemptCtx, cancel = context.WithTimeout(ctx, timeout)
			}

			err := prober.Probe(attemptCtx)
			cancel()

			if ctx.Err() != nil {
				return
			}

			if err == nil {
				successes++
				failures = 0
				if successes >= successNeeded && status != StatusReady {
					status = StatusReady
					event := Event{Status: StatusReady, At: nowFn()}
					if !sendEvent(ctx, events, event) {
						return
					}
				}
			} else {
				if attemptCtx.Err() == context.Canceled && ctx.Err() != nil {
					return
				}
				if attemptCtx.Err() == context.DeadlineExceeded && errors.Is(err, context.DeadlineExceeded) {
					err = fmt.Errorf("timeout after %s", timeout)
				}

				successes = 0
				failures++
				if failures >= failureAllowed && status != StatusUnready {
					status = StatusUnready
					event := Event{Status: StatusUnready, Reason: err.Error(), Err: err, At: nowFn()}
					if !sendEvent(ctx, events, event) {
						return
					}
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

			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	return events
}

func sendEvent(ctx context.Context, events chan<- Event, event Event) bool {
	if events == nil {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case events <- event:
		return true
	}
}

func probeTimeout(spec *stack.Health) time.Duration {
	if spec == nil {
		return 0
	}
	if spec.Command != nil {
		if dur := spec.Command.Timeout.Duration; dur > 0 {
			return dur
		}
	}
	return spec.Timeout.Duration
}
