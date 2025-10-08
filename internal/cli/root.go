package cli

import (
	stdcontext "context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Paintersrp/orco/internal/cliutil"
	"github.com/Paintersrp/orco/internal/engine"
	"github.com/Paintersrp/orco/internal/logmux"
	"github.com/Paintersrp/orco/internal/runtime"
	"github.com/Paintersrp/orco/internal/runtime/docker"
	"github.com/Paintersrp/orco/internal/runtime/process"
	"github.com/Paintersrp/orco/internal/stack"
)

func NewRootCmd() *cobra.Command {
	root, _ := newRootCommand()
	return root
}

func newRootCommand() (*cobra.Command, *context) {
	var stackFile string
	logRetention := logRetentionFromEnv()

	root := &cobra.Command{
		Use:   "orco",
		Short: "Single-node orchestration engine",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}

	root.PersistentFlags().
		StringVarP(&stackFile, "file", "f", "stack.yaml", "Path to stack definition")

	root.PersistentFlags().Var(&stringFlag{value: &logRetention.Directory, set: &logRetention.DirectorySet}, "log-dir", "Directory to persist service logs")
	root.PersistentFlags().Var(&int64Flag{value: &logRetention.MaxFileSize, set: &logRetention.MaxFileSizeSet}, "log-max-file-size", "Maximum size of individual log files before rotation")
	root.PersistentFlags().Var(&int64Flag{value: &logRetention.MaxTotalSize, set: &logRetention.MaxTotalSizeSet}, "log-max-total-size", "Maximum total size of retained log files per service")
	root.PersistentFlags().Var(&durationFlag{value: &logRetention.MaxFileAge, set: &logRetention.MaxFileAgeSet}, "log-max-file-age", "Maximum age of a log file before rotation")
	root.PersistentFlags().Var(&intFlag{value: &logRetention.MaxFileCount, set: &logRetention.MaxFileCountSet}, "log-max-files", "Maximum number of log files to retain per service")

	ctx := &context{stackFile: &stackFile, logRetention: &logRetention}
	root.AddCommand(newUpCmd(ctx))
	root.AddCommand(newDownCmd(ctx))
	root.AddCommand(newStatusCmd(ctx))
	root.AddCommand(newLogsCmd(ctx))
	root.AddCommand(newGraphCmd(ctx))
	root.AddCommand(newRestartCmd(ctx))
	root.AddCommand(newApplyCmd(ctx))
	root.AddCommand(newPromoteCmd(ctx))
	root.AddCommand(newTuiCmd(ctx))
	root.AddCommand(newConfigCmd())

	root.SilenceUsage = true
	root.SilenceErrors = true

	return root, ctx
}

// Execute runs the CLI entrypoint.
func Execute() {
	ctx, stop := signal.NotifyContext(stdcontext.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := NewRootCmd()
	root.SetContext(ctx)

	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type context struct {
	stackFile    *string
	orchestrator *engine.Orchestrator
	logRetention *logRetentionConfig

	mu                  sync.RWMutex
	deployment          *engine.Deployment
	deploymentStackName string
	deploymentStackSpec map[string]*stack.Service
	tracker             *statusTracker
	logStream           *eventStream
}

type logRetentionConfig struct {
	Directory    string
	MaxFileSize  int64
	MaxTotalSize int64
	MaxFileAge   time.Duration
	MaxFileCount int

	DirectorySet    bool
	MaxFileSizeSet  bool
	MaxTotalSizeSet bool
	MaxFileAgeSet   bool
	MaxFileCountSet bool
}

func (c *context) loadStack() (*cliutil.StackDocument, error) {
	doc, err := cliutil.LoadStackFromFile(*c.stackFile)
	if err != nil {
		return nil, err
	}
	c.applyStackLogRetention(doc)
	return doc, nil
}

func (c *context) applyStackLogRetention(doc *cliutil.StackDocument) {
	if doc == nil || doc.File == nil {
		return
	}
	logging := doc.File.Logging
	if logging == nil {
		return
	}
	cfg := c.logRetention
	if cfg == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if logging.Directory != "" && !cfg.DirectorySet {
		cfg.Directory = logging.Directory
	}
	if logging.MaxFileSize != nil && !cfg.MaxFileSizeSet {
		cfg.MaxFileSize = *logging.MaxFileSize
	}
	if logging.MaxTotalSize != nil && !cfg.MaxTotalSizeSet {
		cfg.MaxTotalSize = *logging.MaxTotalSize
	}
	if logging.MaxFileAge.IsSet() && !cfg.MaxFileAgeSet {
		cfg.MaxFileAge = logging.MaxFileAge.Duration
	}
	if logging.MaxFileCount != nil && !cfg.MaxFileCountSet {
		cfg.MaxFileCount = *logging.MaxFileCount
	}
}

func (c *context) getOrchestrator() *engine.Orchestrator {
	if c.orchestrator == nil {
		c.orchestrator = engine.NewOrchestrator(runtime.Registry{
			"docker":  docker.New(),
			"process": process.New(),
		})
	}
	return c.orchestrator
}

func (c *context) setDeployment(dep *engine.Deployment, stackName string, services map[string]*stack.Service) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deployment = dep
	c.deploymentStackName = stackName
	c.deploymentStackSpec = stack.CloneServiceMap(services)
}

func (c *context) clearDeployment(dep *engine.Deployment) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.deployment == dep {
		c.deployment = nil
		c.deploymentStackName = ""
		c.deploymentStackSpec = nil
	}
}

func (c *context) currentDeployment() *engine.Deployment {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.deployment
}

func (c *context) currentDeploymentInfo() (*engine.Deployment, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.deployment, c.deploymentStackName
}

func (c *context) currentDeploymentSpec() map[string]*stack.Service {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.deploymentStackSpec) == 0 {
		return nil
	}
	return stack.CloneServiceMap(c.deploymentStackSpec)
}

func (c *context) statusTracker() *statusTracker {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tracker == nil {
		var opts []StatusTrackerOption
		if value := os.Getenv("ORCO_STATUS_HISTORY"); value != "" {
			if size, err := strconv.Atoi(value); err == nil {
				opts = append(opts, WithHistorySize(size))
			}
		}
		if value := os.Getenv("ORCO_STATUS_JOURNAL"); value != "" {
			if enabled, err := strconv.ParseBool(value); err == nil && enabled {
				opts = append(opts, WithJournalEnabled(true))
				if path := os.Getenv("ORCO_STATUS_JOURNAL_PATH"); path != "" {
					opts = append(opts, WithJournalPath(path))
				}
			}
		}
		c.tracker = newStatusTracker(opts...)
	}
	return c.tracker
}

func (c *context) trackEvents(stackName string, events <-chan engine.Event, buffer int) (<-chan engine.Event, func()) {
	tracker := c.statusTracker()
	if buffer <= 0 {
		buffer = 1
	}

	stream := newEventStream(buffer)

	c.mu.Lock()
	c.logStream = stream
	c.mu.Unlock()

	out := make(chan engine.Event, buffer)
	logOpts := c.logSinkOptions()
	if stackName != "" {
		logOpts = append(logOpts, logmux.WithStackName(stackName))
	}
	logMux := logmux.New(buffer, logOpts...)
	logInput := make(chan engine.Event, buffer)
	logMux.Add(logInput)
	logOutput := logMux.Output()
	var logClosed bool

	go func() {
		defer close(out)
		defer stream.Close()
		defer func() {
			c.mu.Lock()
			if c.logStream == stream {
				c.logStream = nil
			}
			c.mu.Unlock()
		}()

		eventsCh := events
		for eventsCh != nil || logOutput != nil {
			select {
			case evt, ok := <-eventsCh:
				if !ok {
					eventsCh = nil
					if !logClosed {
						close(logInput)
						logMux.Close()
						logClosed = true
					}
					continue
				}
				logInput <- evt
			case evt, ok := <-logOutput:
				if !ok {
					logOutput = nil
					continue
				}
				tracker.Apply(evt)
				out <- evt
				stream.Publish(evt)
			}
		}
		if !logClosed {
			close(logInput)
			logMux.Close()
		}
	}()

	release := func() {
		c.mu.Lock()
		if c.logStream == stream {
			c.logStream = nil
		}
		c.mu.Unlock()
		stream.Close()
	}

	return out, release
}

func (c *context) subscribeLogStream(buffer int) (<-chan engine.Event, func(), bool) {
	c.mu.RLock()
	stream := c.logStream
	c.mu.RUnlock()
	if stream == nil {
		return nil, nil, false
	}
	return stream.Subscribe(buffer)
}

func (c *context) logSinkOptions() []logmux.SinkOption {
	cfg := c.logRetention
	if cfg == nil || cfg.Directory == "" {
		return nil
	}
	opts := []logmux.SinkOption{logmux.WithDirectory(cfg.Directory)}
	if cfg.MaxFileSize > 0 {
		opts = append(opts, logmux.WithMaxFileSize(cfg.MaxFileSize))
	}
	if cfg.MaxTotalSize > 0 {
		opts = append(opts, logmux.WithMaxTotalSize(cfg.MaxTotalSize))
	}
	if cfg.MaxFileAge > 0 {
		opts = append(opts, logmux.WithMaxFileAge(cfg.MaxFileAge))
	}
	if cfg.MaxFileCount > 0 {
		opts = append(opts, logmux.WithMaxFileCount(cfg.MaxFileCount))
	}
	return opts
}

type eventStream struct {
	mu       sync.Mutex
	closed   bool
	subs     map[chan engine.Event]struct{}
	backlog  []engine.Event
	capacity int
}

func newEventStream(capacity int) *eventStream {
	if capacity <= 0 {
		capacity = 1
	}
	return &eventStream{
		subs:     make(map[chan engine.Event]struct{}),
		capacity: capacity,
	}
}

func (s *eventStream) Subscribe(buffer int) (<-chan engine.Event, func(), bool) {
	if buffer <= 0 {
		buffer = 1
	}
	ch := make(chan engine.Event, buffer)

	s.mu.Lock()
	if s.closed {
		close(ch)
		s.mu.Unlock()
		return ch, func() {}, false
	}
	backlog := append([]engine.Event(nil), s.backlog...)
	if s.subs == nil {
		s.subs = make(map[chan engine.Event]struct{})
	}
	s.subs[ch] = struct{}{}
	s.mu.Unlock()

	for _, evt := range backlog {
		select {
		case ch <- evt:
		default:
		}
	}

	release := func() {
		s.mu.Lock()
		if s.subs != nil {
			if _, ok := s.subs[ch]; ok {
				delete(s.subs, ch)
				close(ch)
			}
		}
		s.mu.Unlock()
	}

	return ch, release, true
}

func (s *eventStream) Publish(evt engine.Event) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if evt.Type == engine.EventTypeLog {
		s.backlog = append(s.backlog, evt)
		if len(s.backlog) > s.capacity {
			s.backlog = s.backlog[len(s.backlog)-s.capacity:]
		}
	}
	subscribers := make([]chan engine.Event, 0, len(s.subs))
	for ch := range s.subs {
		subscribers = append(subscribers, ch)
	}
	s.mu.Unlock()

	for _, ch := range subscribers {
		select {
		case ch <- evt:
		default:
		}
	}
}

func (s *eventStream) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	for ch := range s.subs {
		close(ch)
	}
	s.subs = nil
	s.backlog = nil
	s.mu.Unlock()
}

func logRetentionFromEnv() logRetentionConfig {
	cfg := logRetentionConfig{}
	if value, ok := os.LookupEnv("ORCO_LOG_DIR"); ok {
		cfg.Directory = value
		cfg.DirectorySet = value != ""
	}
	if value, ok := os.LookupEnv("ORCO_LOG_MAX_FILE_SIZE"); ok {
		if size, err := strconv.ParseInt(value, 10, 64); err == nil && size >= 0 {
			cfg.MaxFileSize = size
			cfg.MaxFileSizeSet = true
		}
	}
	if value, ok := os.LookupEnv("ORCO_LOG_MAX_TOTAL_SIZE"); ok {
		if size, err := strconv.ParseInt(value, 10, 64); err == nil && size >= 0 {
			cfg.MaxTotalSize = size
			cfg.MaxTotalSizeSet = true
		}
	}
	if value, ok := os.LookupEnv("ORCO_LOG_MAX_FILE_AGE"); ok {
		if age, err := time.ParseDuration(value); err == nil && age >= 0 {
			cfg.MaxFileAge = age
			cfg.MaxFileAgeSet = true
		}
	}
	if value, ok := os.LookupEnv("ORCO_LOG_MAX_FILE_COUNT"); ok {
		if count, err := strconv.Atoi(value); err == nil && count >= 0 {
			cfg.MaxFileCount = count
			cfg.MaxFileCountSet = true
		}
	}
	return cfg
}

type stringFlag struct {
	value *string
	set   *bool
}

func (f *stringFlag) Set(value string) error {
	if f.value != nil {
		*f.value = value
	}
	if f.set != nil {
		*f.set = true
	}
	return nil
}

func (f *stringFlag) String() string {
	if f.value == nil {
		return ""
	}
	return *f.value
}

func (f *stringFlag) Type() string { return "string" }

type int64Flag struct {
	value *int64
	set   *bool
}

func (f *int64Flag) Set(raw string) error {
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return err
	}
	if f.value != nil {
		*f.value = v
	}
	if f.set != nil {
		*f.set = true
	}
	return nil
}

func (f *int64Flag) String() string {
	if f.value == nil {
		return "0"
	}
	return strconv.FormatInt(*f.value, 10)
}

func (f *int64Flag) Type() string { return "int64" }

type intFlag struct {
	value *int
	set   *bool
}

func (f *intFlag) Set(raw string) error {
	v, err := strconv.Atoi(raw)
	if err != nil {
		return err
	}
	if f.value != nil {
		*f.value = v
	}
	if f.set != nil {
		*f.set = true
	}
	return nil
}

func (f *intFlag) String() string {
	if f.value == nil {
		return "0"
	}
	return strconv.Itoa(*f.value)
}

func (f *intFlag) Type() string { return "int" }

type durationFlag struct {
	value *time.Duration
	set   *bool
}

func (f *durationFlag) Set(raw string) error {
	v, err := time.ParseDuration(raw)
	if err != nil {
		return err
	}
	if f.value != nil {
		*f.value = v
	}
	if f.set != nil {
		*f.set = true
	}
	return nil
}

func (f *durationFlag) String() string {
	if f.value == nil {
		return "0s"
	}
	return f.value.String()
}

func (f *durationFlag) Type() string { return "duration" }
