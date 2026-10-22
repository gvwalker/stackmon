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
	return &cobra.Command{
		Use:   "discover",
		Short: "List compose stacks from configured roots and running containers",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cands, err := discoveredStacks(cmd.Context())
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

// discoveredStacks supplies the same names and declarations to discovery and
// enrollment. It performs no inventory mutation and holds no writer lock.
func discoveredStacks(ctx context.Context) ([]discover.Candidate, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	inv, _, err := loadInventory()
	if err != nil {
		return nil, err
	}
	rootCandidates, err := discover.Scan(cfg.Roots, inv)
	if err != nil {
		return nil, err
	}
	var dockerCandidates []discover.Candidate
	prober := local.New()
	if prober.Available() {
		containers, err := prober.Containers(ctx)
		if err != nil {
			return nil, err
		}
		dockerCandidates = discover.DockerCandidates(containers, inv)
	}
	return discover.Merge(inv, rootCandidates, dockerCandidates), nil
}
