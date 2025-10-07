package logmux

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

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

	sink *fileSink
}

type dropRecord struct {
	count   int
	attempt int
}

// New constructs a mux backed by a channel of the provided size. A size of
// zero results in a minimally buffered channel.
func New(size int, opts ...SinkOption) *Mux {
	if size <= 0 {
		size = 1
	}
	cfg := sinkConfig{now: time.Now}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Mux{
		out:   make(chan engine.Event, size),
		drops: make(map[string]dropRecord),
		sink:  newFileSink(cfg),
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
	if m.sink != nil {
		m.sink.Close()
	}
}

func (m *Mux) deliver(evt engine.Event) {
	if !m.flushPending(evt.Service) {
		if evt.Type == engine.EventTypeLog {
			m.write(evt)
		}
		m.recordDrop(evt.Service, evt.Attempt)
		return
	}
	if m.trySend(evt) {
		return
	}
	if evt.Type == engine.EventTypeLog {
		m.write(evt)
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
		if evt.Type == engine.EventTypeLog {
			m.write(evt)
		}
		return true
	default:
		return false
	}
}

func (m *Mux) blockingSend(evt engine.Event) {
	m.out <- evt
	if evt.Type == engine.EventTypeLog {
		m.write(evt)
	}
}

func (m *Mux) write(evt engine.Event) {
	if m.sink == nil {
		return
	}
	_ = m.sink.Write(evt)
}

func normalize(evt engine.Event) engine.Event {
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now()
	}
	if evt.Source == "" {
		evt.Source = runtime.LogSourceStdout
	}
	evt.Level = normalizeLevel(evt.Level, evt.Message)
	return evt
}

var levelTokenPattern = regexp.MustCompile(`(?i)\b(ERROR|WARN|INFO)\b`)

func normalizeLevel(current, message string) string {
	if match := levelTokenPattern.FindStringSubmatch(message); len(match) > 1 {
		return strings.ToLower(match[1])
	}
	if current != "" {
		return strings.ToLower(current)
	}
	return "info"
}

func synthesizeDropEvent(service string, rec dropRecord) engine.Event {
	return engine.Event{
		Timestamp: time.Now(),
		Service:   service,
		Replica:   -1,
		Type:      engine.EventTypeLog,
		Message:   fmt.Sprintf("dropped=%d", rec.count),
		Level:     "warn",
		Source:    runtime.LogSourceSystem,
		Attempt:   rec.attempt,
	}
}

// SinkOption mutates the configuration used to construct a log sink.
type SinkOption func(*sinkConfig)

// WithDirectory configures the sink to store logs beneath the provided
// directory. A per-service subdirectory is created as events are observed.
func WithDirectory(dir string) SinkOption {
	return func(cfg *sinkConfig) {
		cfg.directory = dir
	}
}

// WithStack scopes the sink to the provided stack name, nesting service
// directories beneath the stack directory.
func WithStack(name string) SinkOption {
	return func(cfg *sinkConfig) {
		if name != "" {
			cfg.stack = sanitizeStackName(name)
		}
	}
}

// WithMaxFileSize sets the maximum size of an individual log file before the
// sink performs a rotation. Sizes of zero or less disable size-based rotation.
func WithMaxFileSize(size int64) SinkOption {
	return func(cfg *sinkConfig) {
		if size > 0 {
			cfg.maxFileSize = size
		}
	}
}

// WithMaxTotalSize bounds the total bytes retained for a service. The sink
// prunes the oldest files once the sum of retained log files exceeds the limit.
func WithMaxTotalSize(size int64) SinkOption {
	return func(cfg *sinkConfig) {
		if size > 0 {
			cfg.maxTotalSize = size
		}
	}
}

// WithMaxFileAge specifies the maximum age of a log file before it is rotated
// and pruned. Durations of zero or less disable age-based retention.
func WithMaxFileAge(age time.Duration) SinkOption {
	return func(cfg *sinkConfig) {
		if age > 0 {
			cfg.maxFileAge = age
		}
	}
}

// WithMaxFileCount limits the number of log files retained per service. Values
// of zero or less disable count-based pruning.
func WithMaxFileCount(count int) SinkOption {
	return func(cfg *sinkConfig) {
		if count > 0 {
			cfg.maxFileCount = count
		}
	}
}

type sinkConfig struct {
	directory    string
	stack        string
	maxFileSize  int64
	maxTotalSize int64
	maxFileAge   time.Duration
	maxFileCount int
	now          func() time.Time
}

func newFileSink(cfg sinkConfig) *fileSink {
	if cfg.directory == "" {
		return nil
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	return &fileSink{
		cfg:      cfg,
		services: make(map[string]*serviceSink),
	}
}

type fileSink struct {
	cfg      sinkConfig
	mu       sync.Mutex
	services map[string]*serviceSink
}

func (s *fileSink) Write(evt engine.Event) error {
	if evt.Service == "" {
		return nil
	}
	service := sanitizeComponent(evt.Service)
	s.mu.Lock()
	defer s.mu.Unlock()
	writer, ok := s.services[service]
	if !ok {
		writer = &serviceSink{sink: s, service: service}
		s.services[service] = writer
	}
	return writer.write(evt)
}

func (s *fileSink) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, writer := range s.services {
		writer.close()
		delete(s.services, key)
	}
}

type serviceSink struct {
	sink    *fileSink
	service string
	file    *os.File
	size    int64
	created time.Time
	path    string
}

func (s *serviceSink) write(evt engine.Event) error {
	if err := s.ensureFile(); err != nil {
		return err
	}
	if s.shouldRotate() {
		if err := s.rotate(); err != nil {
			return err
		}
	}
	payload, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	n, err := s.file.Write(payload)
	if err != nil {
		return err
	}
	s.size += int64(n)
	s.prune()
	return nil
}

func (s *serviceSink) ensureFile() error {
	if s.file != nil {
		return nil
	}
	return s.rotate()
}

func (s *serviceSink) rotate() error {
	if s.file != nil {
		_ = s.file.Close()
	}
	dir := filepath.Join(s.sink.directory(), s.service)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	attempt := 0
	for {
		timestamp := s.sink.cfg.now().UTC().Format("20060102T150405.000000000")
		name := timestamp
		if attempt > 0 {
			name = fmt.Sprintf("%s-%d", timestamp, attempt)
		}
		path := filepath.Join(dir, name+".ndjson")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			attempt++
			continue
		}
		if err != nil {
			return err
		}
		s.file = file
		s.size = 0
		s.created = s.sink.cfg.now()
		s.path = path
		break
	}
	return nil
}

func (s *serviceSink) shouldRotate() bool {
	cfg := s.sink.cfg
	if cfg.maxFileSize > 0 && s.size >= cfg.maxFileSize {
		return true
	}
	if cfg.maxFileAge > 0 && !s.created.IsZero() && cfg.now().Sub(s.created) >= cfg.maxFileAge {
		return true
	}
	return false
}

func (s *serviceSink) prune() {
	cfg := s.sink.cfg
	if cfg.maxFileAge <= 0 && cfg.maxTotalSize <= 0 && cfg.maxFileCount <= 0 {
		return
	}
	dir := filepath.Join(s.sink.directory(), s.service)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type info struct {
		entry os.DirEntry
		path  string
		stat  fs.FileInfo
	}
	var files []info
	var total int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		stat, err := entry.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		files = append(files, info{entry: entry, path: path, stat: stat})
		total += stat.Size()
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].stat.ModTime().Before(files[j].stat.ModTime())
	})
	if cfg.maxFileAge > 0 {
		cutoff := cfg.now().Add(-cfg.maxFileAge)
		var retained []info
		for _, file := range files {
			if file.path == s.path {
				retained = append(retained, file)
				continue
			}
			if file.stat.ModTime().Before(cutoff) {
				if err := os.Remove(file.path); err == nil {
					total -= file.stat.Size()
				}
				continue
			}
			retained = append(retained, file)
		}
		files = retained
	}
	if cfg.maxFileCount > 0 {
		remaining := len(files)
		for i := 0; remaining > cfg.maxFileCount && i < len(files); i++ {
			file := files[i]
			if file.path == "" || file.path == s.path {
				continue
			}
			if err := os.Remove(file.path); err == nil {
				total -= file.stat.Size()
				remaining--
				files[i].path = ""
			}
		}
	}
	if cfg.maxTotalSize > 0 {
		i := 0
		for total > cfg.maxTotalSize && i < len(files) {
			file := files[i]
			i++
			if file.path == "" || file.path == s.path {
				continue
			}
			if err := os.Remove(file.path); err == nil {
				total -= file.stat.Size()
				files[i-1].path = ""
			}
		}
	}
}

func (s *serviceSink) close() {
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
}

func (s *fileSink) directory() string {
	if s.cfg.stack != "" {
		return filepath.Join(s.cfg.directory, s.cfg.stack)
	}
	return s.cfg.directory
}

func sanitizeComponent(value string) string {
	return sanitizeName(value, "service")
}

func sanitizeStackName(value string) string {
	return sanitizeName(value, "stack")
}

func sanitizeName(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	value = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			return unicode.ToLower(r)
		case r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, value)
	value = strings.Trim(value, "._")
	if value == "" {
		return fallback
	}
	return value
}

// withClock injects a deterministic clock. It is intended for testing.
func withClock(clock func() time.Time) SinkOption {
	return func(cfg *sinkConfig) {
		if clock != nil {
			cfg.now = clock
		}
	}
}
