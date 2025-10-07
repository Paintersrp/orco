package logmux

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Paintersrp/orco/internal/engine"
	"github.com/Paintersrp/orco/internal/runtime"
)

func TestMuxFansInMultipleSources(t *testing.T) {
	mux := New(4)
	src1 := make(chan engine.Event)
	src2 := make(chan engine.Event)

	mux.Add(src1)
	mux.Add(src2)

	go func() {
		src1 <- engine.Event{Service: "api", Type: engine.EventTypeLog, Message: "api ready"}
		src1 <- engine.Event{Service: "api", Type: engine.EventTypeLog, Message: "api ok"}
		close(src1)
	}()

	go func() {
		src2 <- engine.Event{Service: "worker", Type: engine.EventTypeLog, Message: "worker ready"}
		close(src2)
	}()

	go mux.Close()

	var events []engine.Event
	for evt := range mux.Output() {
		events = append(events, evt)
	}

	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	var apiMessages []string
	var workerMessages []string
	for _, evt := range events {
		switch evt.Service {
		case "api":
			apiMessages = append(apiMessages, evt.Message)
		case "worker":
			workerMessages = append(workerMessages, evt.Message)
		default:
			t.Fatalf("unexpected service %s", evt.Service)
		}
	}

	if len(apiMessages) != 2 {
		t.Fatalf("expected 2 api events, got %d", len(apiMessages))
	}
	if apiMessages[0] != "api ready" || apiMessages[1] != "api ok" {
		t.Fatalf("api messages out of order: %v", apiMessages)
	}
	if len(workerMessages) != 1 || workerMessages[0] != "worker ready" {
		t.Fatalf("unexpected worker messages: %v", workerMessages)
	}
}

func TestMuxPersistsNormalizedEvents(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{now: time.Unix(0, 0)}
	mux := New(4, WithDirectory(dir), withClock(clock.Now))
	src := make(chan engine.Event, 3)
	mux.Add(src)

	go func() {
		src <- engine.Event{Service: "api", Type: engine.EventTypeLog, Message: "INFO: ready"}
		clock.Advance(time.Second)
		src <- engine.Event{Service: "api", Type: engine.EventTypeLog, Message: "stderr", Source: runtime.LogSourceStderr}
		close(src)
	}()

	go mux.Close()

	for range mux.Output() {
	}

	serviceDir := filepath.Join(dir, "api")
	entries, err := os.ReadDir(serviceDir)
	if err != nil {
		t.Fatalf("expected log directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected single log file, got %d", len(entries))
	}

	filePath := filepath.Join(serviceDir, entries[0].Name())
	file, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("open log file: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var records []engine.Event
	for scanner.Scan() {
		var evt engine.Event
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		records = append(records, evt)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	if len(records) != 2 {
		t.Fatalf("expected two events, got %d", len(records))
	}
	if records[0].Level != "info" || records[0].Source != runtime.LogSourceStdout {
		t.Fatalf("unexpected normalization: %+v", records[0])
	}
	if records[1].Source != runtime.LogSourceStderr {
		t.Fatalf("expected stderr source, got %+v", records[1])
	}
	if records[0].Timestamp.IsZero() || records[1].Timestamp.IsZero() {
		t.Fatalf("expected timestamps to be populated")
	}
}

func TestMuxScopesLogsByStack(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{now: time.Unix(0, 0)}
	mux := New(1, WithDirectory(dir), WithStack("Prod Stack"), withClock(clock.Now))
	src := make(chan engine.Event, 1)
	mux.Add(src)

	go func() {
		src <- engine.Event{Service: "api", Type: engine.EventTypeLog, Message: "ready"}
		close(src)
	}()

	mux.Close()
	for range mux.Output() {
	}

	stackDir := filepath.Join(dir, "prod_stack")
	serviceDir := filepath.Join(stackDir, "api")
	entries, err := os.ReadDir(serviceDir)
	if err != nil {
		t.Fatalf("expected stack/service directory hierarchy: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected single log file, got %d", len(entries))
	}
}

func TestMuxRotatesAndPrunesLogs(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{now: time.Unix(0, 0)}
	mux := New(1,
		WithDirectory(dir),
		WithMaxFileSize(120),
		WithMaxTotalSize(240),
		WithMaxFileAge(time.Second),
		withClock(clock.Now),
	)

	src := make(chan engine.Event, 8)
	mux.Add(src)

	go func() {
		for i := 0; i < 6; i++ {
			src <- engine.Event{Service: "api", Type: engine.EventTypeLog, Message: strings.Repeat("x", 64)}
			clock.Advance(500 * time.Millisecond)
		}
		close(src)
	}()

	go mux.Close()
	for range mux.Output() {
	}

	serviceDir := filepath.Join(dir, "api")
	entries, err := os.ReadDir(serviceDir)
	if err != nil {
		t.Fatalf("expected log directory: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected rotated files")
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	var totalSize int64
	now := clock.Now()
	cutoff := now.Add(-time.Second)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatalf("stat file: %v", err)
		}
		totalSize += info.Size()
		if info.ModTime().Before(cutoff) {
			t.Fatalf("expected pruning by age to remove %s", entry.Name())
		}
	}
	if totalSize > 240 {
		t.Fatalf("expected pruning by size, got %d bytes", totalSize)
	}
}

func TestMuxPrunesByFileCount(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{now: time.Unix(0, 0)}
	mux := New(1,
		WithDirectory(dir),
		WithMaxFileSize(64),
		WithMaxFileCount(2),
		withClock(clock.Now),
	)

	src := make(chan engine.Event, 6)
	mux.Add(src)

	go func() {
		for i := 0; i < 5; i++ {
			src <- engine.Event{Service: "api", Type: engine.EventTypeLog, Message: strings.Repeat("x", 64)}
			clock.Advance(250 * time.Millisecond)
		}
		close(src)
	}()

	go mux.Close()
	for range mux.Output() {
	}

	serviceDir := filepath.Join(dir, "api")
	entries, err := os.ReadDir(serviceDir)
	if err != nil {
		t.Fatalf("expected log directory: %v", err)
	}

	if len(entries) != 2 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("expected two retained files, got %d: %v", len(entries), names)
	}
}

func TestMuxPersistsDropMetadata(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{now: time.Unix(0, 0)}
	mux := New(1, WithDirectory(dir), withClock(clock.Now))
	src := make(chan engine.Event)
	mux.Add(src)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 4; i++ {
			src <- engine.Event{Service: "api", Type: engine.EventTypeLog, Message: fmt.Sprintf("line-%d", i)}
			clock.Advance(time.Millisecond)
		}
		close(src)
	}()

	<-done
	go mux.Close()
	for range mux.Output() {
	}

	serviceDir := filepath.Join(dir, "api")
	entries, err := os.ReadDir(serviceDir)
	if err != nil {
		t.Fatalf("expected log directory: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected log file")
	}

	var metaSeen bool
	for _, entry := range entries {
		file, err := os.Open(filepath.Join(serviceDir, entry.Name()))
		if err != nil {
			t.Fatalf("open file: %v", err)
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			var evt engine.Event
			if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if evt.Source == runtime.LogSourceSystem && strings.HasPrefix(evt.Message, "dropped=") {
				metaSeen = true
			}
		}
		file.Close()
	}
	if !metaSeen {
		t.Fatalf("expected drop metadata to be recorded")
	}
}

type fakeClock struct {
	now time.Time
	mu  sync.Mutex
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestMuxEmitsDropMetaEvents(t *testing.T) {
	mux := New(1)
	src := make(chan engine.Event)

	mux.Add(src)

	done := make(chan struct{})
	go func() {
		src <- engine.Event{Service: "api", Type: engine.EventTypeLog, Message: "line-1", Level: "info"}
		src <- engine.Event{Service: "api", Type: engine.EventTypeLog, Message: "line-2", Level: "info"}
		src <- engine.Event{Service: "api", Type: engine.EventTypeLog, Message: "line-3", Level: "info"}
		close(src)
		close(done)
	}()

	<-done

	go mux.Close()

	var events []engine.Event
	for evt := range mux.Output() {
		events = append(events, evt)
	}

	if len(events) < 2 {
		t.Fatalf("expected at least 2 events (1 log + drop metadata), got %d", len(events))
	}

	var logSeen bool
	var metaEvent *engine.Event
	for i := range events {
		evt := events[i]
		if evt.Type == engine.EventTypeLog && evt.Message == "line-1" && !logSeen {
			logSeen = true
		}
		if evt.Message == "dropped=2" && evt.Source == runtime.LogSourceSystem {
			metaEvent = &events[i]
		}
	}
	if !logSeen {
		t.Fatalf("expected to observe the original log entry, events=%v", events)
	}
	if metaEvent == nil {
		t.Fatalf("expected drop metadata event, got %v", events)
	}
	if metaEvent.Service != "api" {
		t.Fatalf("meta event service mismatch: got %s", metaEvent.Service)
	}
	if metaEvent.Level != "warn" {
		t.Fatalf("expected meta level warn, got %s", metaEvent.Level)
	}
	if time.Since(metaEvent.Timestamp) > time.Second {
		t.Fatalf("expected recent timestamp, got %v", metaEvent.Timestamp)
	}
}

func TestMuxNormalizesLevelsFromMessages(t *testing.T) {
	mux := New(4)
	src := make(chan engine.Event, 3)

	mux.Add(src)

	go func() {
		src <- engine.Event{Service: "api", Type: engine.EventTypeLog, Message: "ERROR: failure"}
		src <- engine.Event{Service: "api", Type: engine.EventTypeLog, Message: "INFO: all good", Level: "warn", Source: runtime.LogSourceStderr}
		src <- engine.Event{Service: "api", Type: engine.EventTypeLog, Message: "plain"}
		close(src)
	}()

	go mux.Close()

	var events []engine.Event
	for evt := range mux.Output() {
		events = append(events, evt)
	}

	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	if events[0].Level != "error" {
		t.Fatalf("expected first event level error, got %s", events[0].Level)
	}
	if events[1].Level != "info" {
		t.Fatalf("expected second event level info, got %s", events[1].Level)
	}
	if events[2].Level != "info" {
		t.Fatalf("expected third event level info, got %s", events[2].Level)
	}
	for i, evt := range events {
		if evt.Source == "" {
			t.Fatalf("event %d expected normalized source, got empty", i)
		}
	}
}
