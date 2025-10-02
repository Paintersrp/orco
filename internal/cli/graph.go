package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newGraphCmd(ctx *context) *cobra.Command {
	var dot bool
	cmd := &cobra.Command{
		Use:   "graph",
		Short: "Render the dependency graph",
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := ctx.loadStack()
			if err != nil {
				return err
			}
			if dot {
				fmt.Fprint(cmd.OutOrStdout(), doc.Graph.DOT())
				return nil
			}
			var b strings.Builder
			for _, svc := range doc.Graph.Services() {
				deps := doc.File.Services[svc].DependsOn
				if len(deps) == 0 {
					fmt.Fprintf(&b, "%s\n", svc)
					continue
				}
				fmt.Fprintf(&b, "%s -> ", svc)
				names := make([]string, len(deps))
				for i, dep := range deps {
					names[i] = dep.Target
				}
				fmt.Fprintf(&b, "%s\n", strings.Join(names, ", "))
			}
			fmt.Fprint(cmd.OutOrStdout(), b.String())
			return nil
		},
	}
	cmd.Flags().BoolVar(&dot, "dot", false, "Render in Graphviz DOT format")
	return cmd
}
