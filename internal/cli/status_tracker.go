package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Paintersrp/orco/internal/cliutil"
	"github.com/Paintersrp/orco/internal/engine"
)

// StatusTrackerOption configures a status tracker.
type StatusTrackerOption func(*statusTrackerConfig)

type statusTrackerConfig struct {
	historySize    int
	journalEnabled bool
	journalPath    string
}

func defaultStatusTrackerConfig() statusTrackerConfig {
	return statusTrackerConfig{
		historySize: 20,
	}
}

// WithHistorySize sets the number of transitions retained per service. Values
// less than or equal to zero disable in-memory history tracking.
func WithHistorySize(size int) StatusTrackerOption {
	return func(cfg *statusTrackerConfig) {
		if size <= 0 {
			cfg.historySize = 0
			return
		}
		cfg.historySize = size
	}
}

// WithJournalEnabled toggles persistence of transitions to disk.
func WithJournalEnabled(enabled bool) StatusTrackerOption {
	return func(cfg *statusTrackerConfig) {
		cfg.journalEnabled = enabled
	}
}

// WithJournalPath overrides the path used to persist the journal when
// journaling is enabled. When unset, a default path under the user's home
// directory is used.
func WithJournalPath(path string) StatusTrackerOption {
	return func(cfg *statusTrackerConfig) {
		cfg.journalPath = path
	}
}

func (cfg *statusTrackerConfig) journalFilePath() string {
	if cfg.journalPath != "" {
		return cfg.journalPath
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".orco", "status_journal.jsonl")
}

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

	history         []ServiceTransition
	historyCapacity int
	historyIndex    int
	historyCount    int
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
	cfg      statusTrackerConfig
}

func newStatusTracker(opts ...StatusTrackerOption) *statusTracker {
	cfg := defaultStatusTrackerConfig()
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	return &statusTracker{services: make(map[string]*serviceStatus), cfg: cfg}
}

// ServiceTransition captures a single lifecycle transition for a service.
type ServiceTransition struct {
	Timestamp time.Time
	Type      engine.EventType
	Reason    string
	Message   string
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
		state = newServiceStatus(evt.Service, evt.Timestamp, t.cfg.historySize)
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
		message := formatEventMessage(evt)
		state.message = message
		t.recordTransition(evt, state, message)
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

func formatEventMessage(evt engine.Event) string {
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
	return cliutil.RedactSecrets(message)
}

func newServiceStatus(name string, ts time.Time, historySize int) *serviceStatus {
	status := &serviceStatus{
		name:         name,
		firstSeen:    ts,
		replicas:     make(map[int]*replicaStatus),
		historyIndex: 0,
	}
	if historySize > 0 {
		status.historyCapacity = historySize
		status.history = make([]ServiceTransition, historySize)
	}
	return status
}

func (t *statusTracker) recordTransition(evt engine.Event, state *serviceStatus, message string) {
	if state.historyCapacity > 0 {
		entry := ServiceTransition{
			Timestamp: evt.Timestamp,
			Type:      evt.Type,
			Reason:    evt.Reason,
			Message:   message,
		}
		state.history[state.historyIndex] = entry
		state.historyIndex = (state.historyIndex + 1) % state.historyCapacity
		if state.historyCount < state.historyCapacity {
			state.historyCount++
		}
	}

	if !t.cfg.journalEnabled {
		return
	}
	path := t.cfg.journalFilePath()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()

	payload := map[string]any{
		"timestamp": evt.Timestamp,
		"service":   evt.Service,
		"replica":   evt.Replica,
		"type":      evt.Type,
		"reason":    evt.Reason,
		"message":   message,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	data = append(data, '\n')
	_, _ = f.Write(data)
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

// History returns up to the last n transitions observed for a service in
// chronological order. When the service has fewer stored transitions, all of
// them are returned.
func (t *statusTracker) History(service string, n int) []ServiceTransition {
	if n <= 0 {
		return nil
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	state, ok := t.services[service]
	if !ok || state.historyCount == 0 {
		return nil
	}

	if n > state.historyCount {
		n = state.historyCount
	}

	result := make([]ServiceTransition, n)
	start := state.historyIndex - n
	if start < 0 {
		start += state.historyCapacity
	}
	for i := 0; i < n; i++ {
		idx := (start + i) % state.historyCapacity
		result[i] = state.history[idx]
	}
	return result
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
