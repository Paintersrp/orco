package cli

import (
	stdcontext "context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	apihttp "github.com/Paintersrp/orco/internal/api/http"
)

func newServeCmd(ctx *context) *cobra.Command {
	var apiAddr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run services with the HTTP control API enabled",
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := ctx.loadStack()
			if err != nil {
				return err
			}

			enableAPI := apiEnabled()
			if cmd.Flags().Changed("api") {
				enableAPI = true
			}
			if !enableAPI {
				fmt.Fprintln(cmd.OutOrStdout(), "HTTP API disabled; set ORCO_ENABLE_API=true or pass --api to enable.")
			}

			var hook func(stdcontext.Context) (func() error, error)
			if enableAPI {
				addr := apiAddr
				control := NewControlAPI(ctx)
				if control == nil {
					return errors.New("control API unavailable")
				}
				hook = func(runCtx stdcontext.Context) (func() error, error) {
					server, err := apihttp.NewServer(apihttp.Config{Addr: addr, Controller: control})
					if err != nil {
						return nil, err
					}
					serverCtx, cancel := stdcontext.WithCancel(runCtx)
					errCh := make(chan error, 1)
					go func() {
						errCh <- server.Run(serverCtx)
					}()
					fmt.Fprintf(cmd.OutOrStdout(), "Control API listening on %s\n", server.Addr())
					return func() error {
						cancel()
						err := <-errCh
						if err != nil && !errors.Is(err, stdcontext.Canceled) && !errors.Is(err, http.ErrServerClosed) {
							return err
						}
						return nil
					}, nil
				}
			}

			return runUpNonInteractiveWithHook(cmd, ctx, doc, hook)
		},
	}
	cmd.Flags().StringVar(&apiAddr, "api", ":7663", "address for the HTTP control API (requires ORCO_ENABLE_API or explicit flag)")
	return cmd
}

func apiEnabled() bool {
	value := strings.TrimSpace(os.Getenv("ORCO_ENABLE_API"))
	if value == "" {
		return false
	}
	enabled, err := strconv.ParseBool(value)
	if err != nil {
		return false
	}
	return enabled
}
