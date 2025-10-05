package probe

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Paintersrp/orco/internal/stack"
)

type logProber struct {
	pattern *regexp.Regexp
	sources map[string]struct{}
	levels  map[string]struct{}

	matched atomic.Bool
	notify  chan struct{}
	once    sync.Once
}

func newLogProber(spec *stack.LogProbe) (Prober, error) {
	pattern, err := regexp.Compile(spec.Pattern)
	if err != nil {
		return nil, err
	}

	sources := make(map[string]struct{}, len(spec.Sources))
	for _, src := range spec.Sources {
		lower := strings.ToLower(strings.TrimSpace(src))
		if lower == "" {
			continue
		}
		sources[lower] = struct{}{}
	}

	levels := make(map[string]struct{}, len(spec.Levels))
	for _, lvl := range spec.Levels {
		lower := strings.ToLower(strings.TrimSpace(lvl))
		if lower == "" {
			continue
		}
		levels[lower] = struct{}{}
	}

	return &logProber{
		pattern: pattern,
		sources: sources,
		levels:  levels,
		notify:  make(chan struct{}),
	}, nil
}

func (p *logProber) Probe(ctx context.Context) error {
	if p.matched.Load() {
		return nil
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.notify:
		return nil
	}
}

func (p *logProber) ObserveLog(entry LogEntry) {
	if p.matched.Load() {
		return
	}
	if len(p.sources) > 0 {
		if _, ok := p.sources[strings.ToLower(entry.Source)]; !ok {
			return
		}
	}
	if len(p.levels) > 0 {
		if _, ok := p.levels[strings.ToLower(entry.Level)]; !ok {
			return
		}
	}
	if !p.pattern.MatchString(entry.Message) {
		return
	}
	if p.matched.CompareAndSwap(false, true) {
		p.once.Do(func() { close(p.notify) })
	}
}

func (p *logProber) Ready() bool {
	return p.matched.Load()
}

var _ Prober = (*logProber)(nil)
var _ LogObserver = (*logProber)(nil)
var _ readyReporter = (*logProber)(nil)
