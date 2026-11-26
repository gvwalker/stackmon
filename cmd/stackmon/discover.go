package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gvwalker/stackmon/internal/discover"
	"github.com/gvwalker/stackmon/internal/local"
)

func newDiscoverCmd() *cobra.Command {
	var asJSON, unmanaged bool
	cmd := &cobra.Command{
		Use:   "discover",
		Short: "List compose stacks from configured roots and running containers",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cands, coverage, probeErr, err := discovery(cmd.Context(), unmanaged)
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
				if err := enc.Encode(struct {
					Stacks []stack `json:"stacks"`
					*discover.Coverage
				}{stacks, coverage}); err != nil {
					return err
				}
				return probeErr
			}

			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tSTATE\tPATH")
			for _, s := range stacks {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", s.Name, s.State, s.Path)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if coverage != nil {
				printProjects := func(title string, rows []discover.Project) error {
					fmt.Fprintln(cmd.OutOrStdout(), "\n"+title)
					if len(rows) == 0 {
						fmt.Fprintln(cmd.OutOrStdout(), "[]")
						return nil
					}
					tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
					fmt.Fprintln(tw, "PROJECT\tCONTAINERS\tCOMPOSE PATHS\tCOVERAGE")
					for _, row := range rows {
						paths := strings.Join(row.ComposePaths, ", ")
						if paths == "" {
							paths = "none found"
						}
						coverage := row.Reason
						if coverage == "" {
							coverage = "no enrolled Stack"
						}
						fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", row.Project, strings.Join(row.Containers, ", "), paths, coverage)
					}
					return tw.Flush()
				}
				if err := printProjects("Unmanaged Compose Projects", coverage.Unmanaged); err != nil {
					return err
				}
				if err := printProjects("Projects with uncertain enrollment coverage", coverage.Unknown); err != nil {
					return err
				}
				if !coverage.Complete {
					fmt.Fprintln(cmd.OutOrStdout(), "Unmanaged enumeration incomplete:", coverage.Error)
				}
			}
			return probeErr
		},
	}
	cmd.Flags().BoolVar(&unmanaged, "unmanaged", false, "also report running Compose Projects without enrolled Stacks")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit discovered stacks as JSON")
	return cmd
}

// discoveredStacks supplies the same names and declarations to discovery and
// enrollment. It performs no inventory mutation and holds no writer lock.
func discoveredStacks(ctx context.Context) ([]discover.Candidate, error) {
	cands, _, _, err := discovery(ctx, false)
	return cands, err
}

// discovery uses a single Docker snapshot for both views. Only the requested
// unmanaged report retains filesystem results on Docker failure; enrollment
// and ordinary discovery preserve their existing error behavior.
func discovery(ctx context.Context, unmanaged bool) ([]discover.Candidate, *discover.Coverage, error, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, nil, nil, err
	}
	inv, _, err := loadInventory()
	if err != nil {
		return nil, nil, nil, err
	}
	roots, err := discover.Scan(cfg.Roots, inv)
	if err != nil {
		return nil, nil, nil, err
	}
	var containers []local.Container
	var probeErr error
	prober := local.New()
	if prober.Available() {
		containers, probeErr = prober.ProjectContainers(ctx)
	} else if unmanaged {
		probeErr = fmt.Errorf("Docker is unavailable")
	}
	if probeErr != nil && !unmanaged {
		return nil, nil, nil, probeErr
	}
	var coverage *discover.Coverage
	if unmanaged {
		coverage = discover.Unmanaged(containers, inv)
		if probeErr != nil {
			coverage.Complete = false
			coverage.Error = probeErr.Error()
		}
	}
	return discover.Merge(inv, roots, discover.DockerCandidates(containers, inv)), coverage, probeErr, nil
}
