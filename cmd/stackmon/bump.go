package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/gvwalker/stackmon/internal/bump"
	"github.com/gvwalker/stackmon/internal/compose"
)

func newBumpCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "bump <stack> [service]",
		Short: "Rewrite a stack's image pins to the newest available",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			inv, _, err := loadInventory()
			if err != nil {
				return err
			}
			stacks, err := resolve(inv, args[:1])
			if err != nil {
				return err
			}
			r, parsed, failures, err := buildReport(cmd.Context(), stacks)
			if err != nil {
				return err
			}
			printFailures(cmd, failures)
			if len(parsed) == 0 {
				return fmt.Errorf("%s did not parse; nothing to bump", args[0])
			}

			var only string
			if len(args) == 2 {
				only = args[1]
			}

			byService := map[string]compose.Service{}
			for _, st := range parsed {
				for _, svc := range st.Services {
					byService[svc.Name] = svc
				}
			}

			changed := 0
			for _, img := range r.Images {
				if only != "" && img.Service != only {
					continue
				}
				svc, ok := byService[img.Service]
				if !ok {
					continue
				}
				st := parsed[0]

				change, err := bump.Plan(st, svc, img)
				if err != nil {
					// A refusal is informational, not fatal: other services
					// in the stack may still be bumpable. This includes a
					// candidate whose digest could not be resolved.
					fmt.Fprintln(cmd.ErrOrStderr(), "skipped:", err)
					continue
				}

				if dryRun {
					d, err := bump.Diff(change)
					if err != nil {
						return err
					}
					fmt.Fprint(cmd.OutOrStdout(), d)
					continue
				}
				if err := bump.Apply(change); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "bumped %s/%s: %s -> %s\n", st.Name, svc.Name, change.Old, change.New)
				changed++
			}

			if changed == 0 && !dryRun {
				fmt.Fprintln(cmd.OutOrStdout(), "nothing to bump")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print a unified diff instead of writing")
	return cmd
}
