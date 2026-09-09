package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/gvwalker/stackmon/internal/compose"
	"github.com/gvwalker/stackmon/internal/inventory"
	"github.com/gvwalker/stackmon/internal/local"
	"github.com/gvwalker/stackmon/internal/registry"
	"github.com/gvwalker/stackmon/internal/render"
	"github.com/gvwalker/stackmon/internal/report"
)

// buildReport is shared by check, show, and bump. A stack that fails to
// parse does not abort the run: its failure is collected and returned
// alongside the report built from the stacks that did parse, so one
// unparseable compose file never hides every other stack's status.
func buildReport(ctx context.Context, stacks []inventory.Stack) (report.Report, []compose.Stack, []error, error) {
	cfg, err := loadConfig()
	if err != nil {
		return report.Report{}, nil, nil, err
	}

	var parsed []compose.Stack
	var failures []error
	for _, s := range stacks {
		st, err := compose.Load(ctx, s.Name, s.Dir, s.File)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", s.Name, err))
			continue
		}
		parsed = append(parsed, st)
	}

	r := report.Build(ctx, parsed, report.Options{
		Registry:    registry.New(),
		Docker:      local.New(),
		Config:      cfg,
		Concurrency: cfg.Concurrency,
	})
	return r, parsed, failures, nil
}

// printFailures surfaces per-stack parse failures on stderr. A parse failure
// is not a stackmon failure: it is reported and the run continues.
func printFailures(cmd *cobra.Command, failures []error) {
	for _, f := range failures {
		fmt.Fprintln(cmd.ErrOrStderr(), "error:", f)
	}
}

func newCheckCmd() *cobra.Command {
	var asJSON, failOnUpdate, driftToo bool

	cmd := &cobra.Command{
		Use:   "check [stack...]",
		Short: "Report the status of every image in the enrolled stacks",
		RunE: func(cmd *cobra.Command, args []string) error {
			inv, _, err := loadInventory()
			if err != nil {
				return err
			}
			stacks, err := resolve(inv, args)
			if err != nil {
				return err
			}

			r, _, failures, err := buildReport(cmd.Context(), stacks)
			if err != nil {
				return err
			}
			// Printed to stderr first so stdout stays a clean table for
			// piping, regardless of --json.
			printFailures(cmd, failures)

			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(r); err != nil {
					return err
				}
			} else if err := render.Table(cmd.OutOrStdout(), r); err != nil {
				return err
			}

			if failOnUpdate && (r.HasUpdates() || (driftToo && r.HasDrift())) {
				return errUpdatesFound
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the report as JSON")
	cmd.Flags().BoolVar(&failOnUpdate, "fail-on-update", false, "exit 2 when an update is available")
	cmd.Flags().BoolVar(&driftToo, "drift-too", false, "with --fail-on-update, also exit 2 on digest drift")
	return cmd
}
