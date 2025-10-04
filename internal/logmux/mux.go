package logmux

import (
	"fmt"
	"sync"
	"time"

	"github.com/Paintersrp/orco/internal/engine"
	"github.com/Paintersrp/orco/internal/runtime"
)

// Mux fans in log events from multiple services and delivers them via a bounded
// channel. When downstream consumers cannot keep up and the output buffer would
// overflow, the mux drops log records and emits a synthesized warning event to
// surface the number of discarded entries.
type Mux struct {
	out chan engine.Event

	mu     sync.Mutex
	drops  map[string]dropRecord
	inputs sync.WaitGroup
}

type dropRecord struct {
	count   int
	attempt int
}

// New constructs a mux backed by a channel of the provided size. A size of
// zero results in a minimally buffered channel.
func New(size int) *Mux {
	if size <= 0 {
		size = 1
	}
	return &Mux{
		out:   make(chan engine.Event, size),
		drops: make(map[string]dropRecord),
	}
}

// Output exposes the muxed event channel.
func (m *Mux) Output() <-chan engine.Event {
	return m.out
}

// Add registers a new source channel. The mux consumes log events until the
// source channel is closed.
func (m *Mux) Add(source <-chan engine.Event) {
	if source == nil {
		return
	}
	m.inputs.Add(1)
	go func() {
		defer m.inputs.Done()
		for evt := range source {
			if evt.Type != engine.EventTypeLog {
				continue
			}
			m.deliver(normalize(evt))
		}
	}()
}

// Close waits for all sources to be drained, emits any pending drop metadata,
// and closes the output channel.
func (m *Mux) Close() {
	m.inputs.Wait()
	m.flushDrops()
	close(m.out)
}

func (m *Mux) deliver(evt engine.Event) {
	if !m.flushPending(evt.Service) {
		m.recordDrop(evt.Service, evt.Attempt)
		return
	}
	if m.trySend(evt) {
		return
	}
	m.recordDrop(evt.Service, evt.Attempt)
}

func (m *Mux) flushPending(service string) bool {
	for {
		rec := m.takeDrops(service)
		if rec.count == 0 {
			return true
		}
		meta := synthesizeDropEvent(service, rec)
		if m.trySend(meta) {
			continue
		}
		m.recordDropWithCount(service, rec.count, rec.attempt)
		return false
	}
}

func (m *Mux) takeDrops(service string) dropRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := m.drops[service]
	if rec.count != 0 {
		delete(m.drops, service)
	}
	return rec
}

func (m *Mux) recordDrop(service string, attempt int) {
	m.recordDropWithCount(service, 1, attempt)
}

func (m *Mux) recordDropWithCount(service string, count int, attempt int) {
	if count <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := m.drops[service]
	rec.count += count
	if attempt != 0 || rec.attempt == 0 {
		rec.attempt = attempt
	}
	m.drops[service] = rec
}

func (m *Mux) flushDrops() {
	pending := m.collectDrops()
	for service, rec := range pending {
		meta := synthesizeDropEvent(service, rec)
		m.blockingSend(meta)
	}
}

func (m *Mux) collectDrops() map[string]dropRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.drops) == 0 {
		return nil
	}
	dup := make(map[string]dropRecord, len(m.drops))
	for svc, rec := range m.drops {
		if rec.count == 0 {
			continue
		}
		dup[svc] = rec
	}
	m.drops = make(map[string]dropRecord)
	return dup
}

func (m *Mux) trySend(evt engine.Event) bool {
	select {
	case m.out <- evt:
		return true
	default:
		return false
	}
}

func (m *Mux) blockingSend(evt engine.Event) {
	m.out <- evt
}

func normalize(evt engine.Event) engine.Event {
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now()
	}
	if evt.Source == "" {
		evt.Source = runtime.LogSourceStdout
	}
	if evt.Level == "" {
		if evt.Source == runtime.LogSourceStderr {
			evt.Level = "warn"
		} else {
			evt.Level = "info"
		}
	}
	return evt
}

func synthesizeDropEvent(service string, rec dropRecord) engine.Event {
	return engine.Event{
		Timestamp: time.Now(),
		Service:   service,
		Replica:   0,
		Type:      engine.EventTypeLog,
		Message:   fmt.Sprintf("dropped=%d", rec.count),
		Level:     "warn",
		Source:    runtime.LogSourceSystem,
		Attempt:   rec.attempt,
	}
}
