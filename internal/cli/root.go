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

	mu         sync.RWMutex
	deployment *engine.Deployment
	tracker    *statusTracker
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

func (c *context) setDeployment(dep *engine.Deployment) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deployment = dep
}

func (c *context) clearDeployment(dep *engine.Deployment) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.deployment == dep {
		c.deployment = nil
	}
}

func (c *context) currentDeployment() *engine.Deployment {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.deployment
}

func (c *context) statusTracker() *statusTracker {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tracker == nil {
		c.tracker = newStatusTracker()
	}
	return c.tracker
}

func (c *context) trackEvents(events <-chan engine.Event, buffer int) <-chan engine.Event {
	tracker := c.statusTracker()
	var out chan engine.Event
	if buffer > 0 {
		out = make(chan engine.Event, buffer)
	} else {
		out = make(chan engine.Event)
	}
	go func() {
		defer close(out)
		for evt := range events {
			tracker.Apply(evt)
			out <- evt
		}
	}()
	return out
}
