package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Paintersrp/orco/internal/config"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Work with stack configuration files",
	}
	cmd.AddCommand(newConfigLintCmd())
	return cmd
}

func newConfigLintCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lint",
		Short: "Validate a stack configuration file",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "stack.yaml"
			if flag := cmd.Flag("file"); flag != nil {
				if value := flag.Value.String(); value != "" {
					path = value
				}
			} else if inherited := cmd.InheritedFlags().Lookup("file"); inherited != nil {
				if value := inherited.Value.String(); value != "" {
					path = value
				}
			}

			doc, err := config.Load(path)
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), err)
				return err
			}

			for _, warning := range doc.Warnings {
				fmt.Fprintf(cmd.OutOrStdout(), "warning: %s\n", warning)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "%s: OK\n", path)
			return nil
		},
	}
	return cmd
}
