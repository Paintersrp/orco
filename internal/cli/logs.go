package cli

import (
        "encoding/json"
        "fmt"
        "strings"
        "time"

        "github.com/spf13/cobra"

        "github.com/Paintersrp/orco/internal/cliutil"
        "github.com/Paintersrp/orco/internal/engine"
)

func newLogsCmd(ctx *context) *cobra.Command {
        var (
                follow bool
                since  time.Duration
                outputFormat string
        )
        cmd := &cobra.Command{
                Use:   "logs [service]",
                Short: "Tail structured logs",
                Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := ctx.loadStack()
			if err != nil {
				return err
			}
			var filter string
			if len(args) == 1 {
				filter = args[0]
				if _, ok := doc.File.Services[filter]; !ok {
					return fmt.Errorf("unknown service %s", filter)
				}
			}

			const logBufferSize = 256
			var cutoff time.Time
			if since > 0 {
				cutoff = time.Now().Add(-since)
			}

                        events, release, ok := ctx.subscribeLogStream(logBufferSize)
                        if !ok {
                                _, stackName := ctx.currentDeploymentInfo()
                                if stackName != "" {
                                        return fmt.Errorf("stack %s has no active deployment", stackName)
                                }
                                return fmt.Errorf("no active deployment: start services with \"orco up\" first")
                        }
                        defer release()

                        format := strings.ToLower(outputFormat)
                        if format == "" {
                                format = "json"
                        }

                        if format != "json" {
                                return fmt.Errorf("unsupported output format %q", format)
                        }

                        encoder := json.NewEncoder(cmd.OutOrStdout())

			process := func(event engine.Event) {
				if event.Type != engine.EventTypeLog {
					return
				}
				if filter != "" && event.Service != filter {
					return
				}
				if !cutoff.IsZero() {
					ts := event.Timestamp
					if ts.IsZero() {
						ts = time.Now()
					}
					if ts.Before(cutoff) {
						return
					}
				}
				cliutil.EncodeLogEvent(encoder, cmd.ErrOrStderr(), event)
			}

			drainBuffered := func() bool {
				for {
					select {
					case evt, ok := <-events:
						if !ok {
							return true
						}
						process(evt)
					default:
						return false
					}
				}
			}

			if closed := drainBuffered(); closed {
				return nil
			}

			if !follow {
				return nil
			}

			for {
				select {
				case <-cmd.Context().Done():
					return nil
				case evt, ok := <-events:
					if !ok {
						return nil
					}
					process(evt)
					if closed := drainBuffered(); closed {
						return nil
					}
				}
			}
		},
        }
        cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output")
        cmd.Flags().DurationVar(&since, "since", 0, "Only include log entries newer than this duration")
        cmd.Flags().StringVarP(&outputFormat, "output", "o", "json", "Output format (json)")
        return cmd
}
