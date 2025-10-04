package cli

import (
	"sort"
	"sync"
	"time"

	"github.com/Paintersrp/orco/internal/engine"
)

// serviceStatus captures runtime state for a service observed via events.
type serviceStatus struct {
	name      string
	firstSeen time.Time
	lastEvent time.Time
	state     engine.EventType
	ready     bool
	restarts  int
	message   string
}

// statusTracker maintains in-memory status for services based on engine events.
type statusTracker struct {
	mu       sync.RWMutex
	services map[string]*serviceStatus
}

func newStatusTracker() *statusTracker {
	return &statusTracker{services: make(map[string]*serviceStatus)}
}

// Apply updates the tracker based on the supplied event.
func (t *statusTracker) Apply(evt engine.Event) {
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now()
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	state := t.services[evt.Service]
	if state == nil {
		state = &serviceStatus{name: evt.Service, firstSeen: evt.Timestamp}
		t.services[evt.Service] = state
	}
	if state.firstSeen.IsZero() {
		state.firstSeen = evt.Timestamp
	}
	state.lastEvent = evt.Timestamp

	if evt.Type != engine.EventTypeLog {
		state.state = evt.Type
		switch evt.Type {
		case engine.EventTypeReady:
			state.ready = true
		case engine.EventTypeUnready, engine.EventTypeStopping, engine.EventTypeStopped,
			engine.EventTypeCrashed, engine.EventTypeFailed, engine.EventTypeBlocked:
			state.ready = false
		}
		if evt.Type == engine.EventTypeCrashed {
			state.restarts++
		}
		if evt.Message != "" {
			state.message = evt.Message
		} else if evt.Err != nil {
			state.message = evt.Err.Error()
		} else {
			state.message = ""
		}
	}
}

// ServiceStatus captures a snapshot of a service state for presentation.
type ServiceStatus struct {
	Name      string
	FirstSeen time.Time
	LastEvent time.Time
	State     engine.EventType
	Ready     bool
	Restarts  int
	Message   string
}

// Snapshot returns a map keyed by service name containing copies of the tracked state.
func (t *statusTracker) Snapshot() map[string]ServiceStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()

	snapshot := make(map[string]ServiceStatus, len(t.services))
	for name, state := range t.services {
		snapshot[name] = ServiceStatus{
			Name:      state.name,
			FirstSeen: state.firstSeen,
			LastEvent: state.lastEvent,
			State:     state.state,
			Ready:     state.ready,
			Restarts:  state.restarts,
			Message:   state.message,
		}
	}
	return snapshot
}

// Names returns the list of known services sorted alphabetically. Useful for tests.
func (t *statusTracker) Names() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	names := make([]string, 0, len(t.services))
	for name := range t.services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
