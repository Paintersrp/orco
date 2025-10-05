package cli

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Paintersrp/orco/internal/cliutil"
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

	replicaCount  int
	replicas      map[int]*replicaStatus
	readyReplicas int
}

type replicaStatus struct {
	ready    bool
	restarts int
	state    engine.EventType
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
		state = &serviceStatus{name: evt.Service, firstSeen: evt.Timestamp, replicas: make(map[int]*replicaStatus)}
		t.services[evt.Service] = state
	}
	if state.replicas == nil {
		state.replicas = make(map[int]*replicaStatus)
	}
	if state.firstSeen.IsZero() {
		state.firstSeen = evt.Timestamp
	}
	if evt.Timestamp.After(state.lastEvent) {
		state.lastEvent = evt.Timestamp
	}

	if evt.Replica >= 0 {
		rep := state.replicas[evt.Replica]
		if rep == nil {
			rep = &replicaStatus{}
			state.replicas[evt.Replica] = rep
		}
		if evt.Type != engine.EventTypeLog {
			rep.state = evt.Type
			switch evt.Type {
			case engine.EventTypeReady:
				rep.ready = true
			case engine.EventTypeUnready, engine.EventTypeStopping, engine.EventTypeStopped,
				engine.EventTypeCrashed, engine.EventTypeFailed:
				rep.ready = false
			}
			if evt.Type == engine.EventTypeCrashed {
				rep.restarts++
			}
		}
		if evt.Replica+1 > state.replicaCount {
			state.replicaCount = evt.Replica + 1
		}
	}

	if evt.Type != engine.EventTypeLog {
		state.state = evt.Type
		message := ""
		if evt.Message != "" {
			message = evt.Message
		} else if evt.Err != nil {
			message = evt.Err.Error()
		}
		if evt.Replica >= 0 && message != "" {
			message = fmt.Sprintf("replica %d: %s", evt.Replica, message)
		}
		if evt.Replica >= 0 && message == "" {
			message = fmt.Sprintf("replica %d", evt.Replica)
		}
		state.message = cliutil.RedactSecrets(message)
	}

	totalRestarts := 0
	ready := state.replicaCount > 0
	readyCount := 0
	for i := 0; i < state.replicaCount; i++ {
		rep := state.replicas[i]
		if rep == nil || !rep.ready {
			ready = false
		} else {
			readyCount++
		}
		if rep != nil {
			totalRestarts += rep.restarts
		}
	}
	if state.replicaCount == 0 && (evt.Replica < 0 || evt.Type == engine.EventTypeBlocked) {
		ready = false
	}
	state.ready = ready
	state.restarts = totalRestarts
	state.readyReplicas = readyCount
	if state.state == engine.EventTypeReady && !state.ready {
		state.state = engine.EventTypeStarting
	}
}

// ServiceStatus captures a snapshot of a service state for presentation.
type ServiceStatus struct {
	Name          string
	FirstSeen     time.Time
	LastEvent     time.Time
	State         engine.EventType
	Ready         bool
	Restarts      int
	Replicas      int
	ReadyReplicas int
	Message       string
}

// Snapshot returns a map keyed by service name containing copies of the tracked state.
func (t *statusTracker) Snapshot() map[string]ServiceStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()

	snapshot := make(map[string]ServiceStatus, len(t.services))
	for name, state := range t.services {
		snapshot[name] = ServiceStatus{
			Name:          state.name,
			FirstSeen:     state.firstSeen,
			LastEvent:     state.lastEvent,
			State:         state.state,
			Ready:         state.ready,
			Restarts:      state.restarts,
			Replicas:      state.replicaCount,
			ReadyReplicas: state.readyReplicas,
			Message:       state.message,
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
