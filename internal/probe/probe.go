package probe

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Paintersrp/orco/internal/stack"
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

// LogEntry represents a single log line observed by a log-aware probe.
type LogEntry struct {
	Message string
	Source  string
	Level   string
}

// LogObserver consumes log entries surfaced by runtimes to drive readiness
// evaluations.
type LogObserver interface {
	ObserveLog(LogEntry)
}

type readyReporter interface {
	Ready() bool
}

// New constructs an implementation of Prober for the supplied specification.
func New(spec *stack.Health) (Prober, error) {
	if spec == nil {
		return nil, nil
	}
	probes := make(map[string]Prober, 4)

	if spec.HTTP != nil {
		probes["http"] = newHTTPProber(spec.HTTP)
	}
	if spec.TCP != nil {
		probes["tcp"] = newTCPProber(spec.TCP)
	}
	if spec.Command != nil {
		prober, err := newCommandProber(spec.Command)
		if err != nil {
			return nil, err
		}
		probes["cmd"] = prober
	}
	if spec.Log != nil {
		prober, err := newLogProber(spec.Log)
		if err != nil {
			return nil, err
		}
		probes["log"] = prober
	}

	if len(probes) == 0 {
		return nil, errors.New("probe: missing configuration")
	}

	order, err := resolveProbeOrder(spec.Expression, probes)
	if err != nil {
		return nil, err
	}
	if len(order) == 1 {
		return probes[order[0]], nil
	}
	return newMultiProber(order, probes), nil
}

func resolveProbeOrder(expression string, probes map[string]Prober) ([]string, error) {
	if strings.TrimSpace(expression) == "" {
		order := make([]string, 0, len(probes))
		for _, alias := range []string{"http", "tcp", "cmd", "log"} {
			if _, ok := probes[alias]; ok {
				order = append(order, alias)
			}
		}
		if len(order) == 0 {
			return nil, errors.New("probe: missing configuration")
		}
		return order, nil
	}

	tokens, err := parseExpression(expression)
	if err != nil {
		return nil, fmt.Errorf("probe: %w", err)
	}

	order := make([]string, 0, len(tokens))
	seen := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		if _, ok := probes[token]; !ok {
			return nil, fmt.Errorf("probe: expression references undefined probe %q", token)
		}
		if _, dup := seen[token]; dup {
			continue
		}
		seen[token] = struct{}{}
		order = append(order, token)
	}
	if len(order) == 0 {
		return nil, errors.New("probe: missing probes in expression")
	}
	return order, nil
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

type multiProber struct {
	terms     []probeTerm
	observers []LogObserver
}

type probeTerm struct {
	alias string
	probe Prober
	ready readyReporter
}

func newMultiProber(order []string, probes map[string]Prober) Prober {
	terms := make([]probeTerm, 0, len(order))
	observers := make([]LogObserver, 0, len(order))
	for _, alias := range order {
		prober := probes[alias]
		term := probeTerm{alias: alias, probe: prober}
		if rr, ok := prober.(readyReporter); ok {
			term.ready = rr
		}
		if observer, ok := prober.(LogObserver); ok {
			observers = append(observers, observer)
		}
		terms = append(terms, term)
	}
	return &multiProber{terms: terms, observers: observers}
}

func (m *multiProber) ObserveLog(entry LogEntry) {
	for _, observer := range m.observers {
		observer.ObserveLog(entry)
	}
}

func (m *multiProber) Probe(ctx context.Context) error {
	for _, term := range m.terms {
		if term.ready != nil && term.ready.Ready() {
			return nil
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type result struct {
		alias string
		err   error
	}

	results := make(chan result, len(m.terms))
	for _, term := range m.terms {
		go func(alias string, prober Prober) {
			err := prober.Probe(ctx)
			results <- result{alias: alias, err: err}
		}(term.alias, term.probe)
	}

	var errs []error
	for i := 0; i < len(m.terms); i++ {
		select {
		case <-ctx.Done():
			if ctxErr := ctx.Err(); ctxErr != nil && len(errs) == 0 {
				return ctxErr
			}
		case res := <-results:
			if res.err == nil {
				cancel()
				return nil
			}
			errs = append(errs, fmt.Errorf("%s: %w", res.alias, res.err))
		}
	}

	if len(errs) == 0 {
		return errors.New("no probes executed")
	}
	return errors.Join(errs...)
}

func parseExpression(expr string) ([]string, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return nil, errors.New("expression is empty")
	}
	tokens := strings.Fields(trimmed)
	if len(tokens) == 0 {
		return nil, errors.New("expression is empty")
	}
	expectProbe := true
	refs := make([]string, 0, (len(tokens)+1)/2)
	for _, token := range tokens {
		lower := strings.ToLower(token)
		if expectProbe {
			switch lower {
			case "http", "tcp", "cmd", "log":
				refs = append(refs, lower)
				expectProbe = false
			default:
				return nil, fmt.Errorf("invalid probe reference %q", token)
			}
			continue
		}
		if lower != "or" && token != "||" {
			return nil, fmt.Errorf("unsupported operator %q", token)
		}
		expectProbe = true
	}
	if expectProbe {
		return nil, errors.New("expression is incomplete")
	}
	return refs, nil
}
