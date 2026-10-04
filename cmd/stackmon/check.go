package main

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/gvwalker/stackmon/internal/render"
)

func newCheckCmd() *cobra.Command {
	var asJSON, compact, failOnUpdate, driftToo, failOnIncomplete bool

	cmd := &cobra.Command{
		Use:   "check [stack...]",
		Short: "Report the status of every image in the enrolled stacks",
		RunE: func(cmd *cobra.Command, args []string) error {
			r, _, err := loadStackReport(cmd, args)
			if err != nil {
				return err
			}

			switch {
			case compact:
				// --compact is a JSON form, not a formatting option, so it
				// stands on its own and --json adds nothing to it.
				if err := render.CompactJSON(cmd.OutOrStdout(), r); err != nil {
					return err
				}
			case asJSON:
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(r); err != nil {
					return err
				}
			default:
				if err := render.Table(cmd.OutOrStdout(), r); err != nil {
					return err
				}
			}

			if failOnUpdate && (r.HasUpdates() || (driftToo && r.HasDrift())) {
				return errUpdatesFound
			}
			if failOnIncomplete && r.Incomplete() {
				return errIncompleteChecks
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the report as JSON")
	cmd.Flags().BoolVarP(&compact, "compact", "c", false, "emit JSON carrying only the actionable fields")
	cmd.Flags().BoolVar(&failOnUpdate, "fail-on-update", false, "exit 2 when an update is available")
	cmd.Flags().BoolVar(&driftToo, "drift-too", false, "with --fail-on-update, also exit 2 on digest drift")
	cmd.Flags().BoolVar(&failOnIncomplete, "fail-on-incomplete", false, "exit 3 when a required check was skipped or failed (missing paths, parse failures, failed Docker probes)")
	cmd.ValidArgsFunction = completeEnrolledStacks
	return cmd
}
