package cli

import (
	stdcontext "context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Paintersrp/orco/internal/cliutil"
	"github.com/Paintersrp/orco/internal/engine"
	"github.com/Paintersrp/orco/internal/runtime"
	"github.com/Paintersrp/orco/internal/runtime/docker"
	"github.com/Paintersrp/orco/internal/runtime/process"
)

func NewRootCmd() *cobra.Command {
	var stackFile string

	root := &cobra.Command{
		Use:   "orco",
		Short: "Single-node orchestration engine",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}

	root.PersistentFlags().
		StringVarP(&stackFile, "file", "f", "stack.yaml", "Path to stack definition")

	ctx := &context{stackFile: &stackFile}
	root.AddCommand(newUpCmd(ctx))
	root.AddCommand(newDownCmd(ctx))
	root.AddCommand(newStatusCmd(ctx))
	root.AddCommand(newLogsCmd(ctx))
	root.AddCommand(newGraphCmd(ctx))
	root.AddCommand(newRestartCmd(ctx))
	root.AddCommand(newApplyCmd(ctx))
	root.AddCommand(newTuiCmd(ctx))
	root.AddCommand(newConfigCmd())

	root.SilenceUsage = true
	root.SilenceErrors = true

	return root
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

	mu                  sync.RWMutex
	deployment          *engine.Deployment
	deploymentStackName string
	tracker             *statusTracker
	logStream           *eventStream
}

func (c *context) loadStack() (*cliutil.StackDocument, error) {
	return cliutil.LoadStackFromFile(*c.stackFile)
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

func (c *context) setDeployment(dep *engine.Deployment, stackName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deployment = dep
	c.deploymentStackName = stackName
}

func (c *context) clearDeployment(dep *engine.Deployment) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.deployment == dep {
		c.deployment = nil
		c.deploymentStackName = ""
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

func (c *context) statusTracker() *statusTracker {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tracker == nil {
		c.tracker = newStatusTracker()
	}
	return c.tracker
}

func (c *context) trackEvents(events <-chan engine.Event, buffer int) (<-chan engine.Event, func()) {
	tracker := c.statusTracker()
	if buffer <= 0 {
		buffer = 1
	}

	stream := newEventStream(buffer)

	c.mu.Lock()
	c.logStream = stream
	c.mu.Unlock()

	out := make(chan engine.Event, buffer)
	go func() {
		defer close(out)
		defer stream.Close()
		for evt := range events {
			tracker.Apply(evt)
			out <- evt
			stream.Publish(evt)
		}
		c.mu.Lock()
		if c.logStream == stream {
			c.logStream = nil
		}
		c.mu.Unlock()
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
