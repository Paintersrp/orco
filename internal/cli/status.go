package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

func newStatusCmd(ctx *context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Display a summary of services defined in the stack",
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := ctx.loadStack()
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "SERVICE\tRUNTIME\tREPLICAS\tDEPENDENCIES")
			for _, name := range doc.File.ServicesSorted() {
				svc := doc.File.Services[name]
				deps := "-"
				if len(svc.DependsOn) > 0 {
					items := make([]string, len(svc.DependsOn))
					for i, dep := range svc.DependsOn {
						require := dep.Require
						if require == "" {
							require = "ready"
						}
						items[i] = fmt.Sprintf("%s(%s)", dep.Target, require)
					}
					deps = strings.Join(items, ", ")
				}
				fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", name, svc.Runtime, svc.Replicas, deps)
			}
			w.Flush()
			fmt.Fprintf(cmd.OutOrStdout(), "\nStack: %s (version %s)\n", doc.File.Stack.Name, doc.File.Version)
			fmt.Fprintf(cmd.OutOrStdout(), "Validated at %s\n", time.Now().Format(time.RFC3339))
			return nil
		},
	}
	return cmd
}
