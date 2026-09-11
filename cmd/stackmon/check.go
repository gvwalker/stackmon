package main

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/gvwalker/stackmon/internal/render"
)

func newCheckCmd() *cobra.Command {
	var asJSON, failOnUpdate, driftToo bool

	cmd := &cobra.Command{
		Use:   "check [stack...]",
		Short: "Report the status of every image in the enrolled stacks",
		RunE: func(cmd *cobra.Command, args []string) error {
			r, _, err := loadStackReport(cmd, args)
			if err != nil {
				return err
			}

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
	cmd.ValidArgsFunction = completeEnrolledStacks
	return cmd
}
