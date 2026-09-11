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
			stacks, missing, err := resolve(inv, args[:1])
			if err != nil {
				return err
			}
			r, parsed, failures, err := buildReport(cmd.Context(), stacks)
			if err != nil {
				return err
			}
			printFailures(cmd, missing)
			printFailures(cmd, failures)

			var only string
			if len(args) == 2 {
				only = args[1]
			}

			// Keyed by service name alone: safe only because resolve above
			// resolved exactly one stack. A future multi-stack bump must key
			// by stack+service, or two same-named services in different
			// stacks would collide and one stack's plan could be applied to
			// the other.
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
	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return completeEnrolledStacks(cmd, args, toComplete)
		}
		if len(args) == 1 {
			return completeStackServices(cmd, args[0], toComplete)
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return cmd
}
