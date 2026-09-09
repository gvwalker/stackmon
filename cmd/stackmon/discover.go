package main

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gvwalker/stackmon/internal/discover"
)

func newDiscoverCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "discover",
		Short: "List compose stacks under the configured roots",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			inv, _, err := loadInventory()
			if err != nil {
				return err
			}
			if len(cfg.Roots) == 0 {
				return fmt.Errorf("no roots configured; add roots = [...] to your config file")
			}

			cands, err := discover.Scan(cfg.Roots, inv)
			if err != nil {
				return err
			}

			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tSTATE\tPATH")
			for _, c := range cands {
				state := "available"
				if c.Enrolled {
					state = "enrolled"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s/%s\n", c.Name, state, c.Dir, c.File)
			}
			return tw.Flush()
		},
	}
}
