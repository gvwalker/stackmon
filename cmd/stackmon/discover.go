package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gvwalker/stackmon/internal/discover"
	"github.com/gvwalker/stackmon/internal/local"
)

func newDiscoverCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "discover",
		Short: "List compose stacks from configured roots and running containers",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cands, err := discoveredStacks(cmd.Context())
			if err != nil {
				return err
			}

			type stack struct {
				Name     string `json:"name"`
				Path     string `json:"path"`
				Enrolled bool   `json:"enrolled"`
				State    string `json:"state"`
			}
			stacks := make([]stack, 0, len(cands))
			for _, c := range cands {
				state := "available"
				if c.Enrolled {
					state = "enrolled"
				}
				stacks = append(stacks, stack{c.Name, fmt.Sprintf("%s/%s", c.Dir, c.File), c.Enrolled, state})
			}
			if asJSON {
				for i := range stacks {
					path, err := filepath.Abs(stacks[i].Path)
					if err != nil {
						return err
					}
					stacks[i].Path = path
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(struct {
					Stacks []stack `json:"stacks"`
				}{stacks})
			}

			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tSTATE\tPATH")
			for _, s := range stacks {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", s.Name, s.State, s.Path)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit discovered stacks as JSON")
	return cmd
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
