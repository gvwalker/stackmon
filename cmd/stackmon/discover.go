package main

import (
	"context"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gvwalker/stackmon/internal/discover"
	"github.com/gvwalker/stackmon/internal/local"
)

func newDiscoverCmd() *cobra.Command {
	return newDiscoverCmdWithProber(local.New())
}

func newDiscoverCmdWithProber(prober local.Prober) *cobra.Command {
	return &cobra.Command{
		Use:   "discover",
		Short: "List compose stacks from configured roots and running containers",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			inv, _, err := loadInventory()
			if err != nil {
				return err
			}

			rootCandidates, err := discover.Scan(cfg.Roots, inv)
			if err != nil {
				return err
			}
			var dockerCandidates []discover.Candidate
			if prober.Available() {
				containers, err := prober.Containers(context.Background())
				if err != nil {
					return err
				}
				dockerCandidates = discover.DockerCandidates(containers, inv)
			}
			cands := discover.Merge(inv, rootCandidates, dockerCandidates)

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
