package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/gvwalker/stackmon/internal/bump"
	"github.com/gvwalker/stackmon/internal/compose"
	"github.com/gvwalker/stackmon/internal/imageref"
)

// plannedChange is one service's rewrite, kept with the name to report it
// under, since a change carries only a path.
type plannedChange struct {
	stack   string
	service string
	change  bump.Change
}

func newBumpCmd() *cobra.Command {
	var dryRun, digest bool

	cmd := &cobra.Command{
		Use:   "bump <stack> [service]",
		Short: "Rewrite a stack's image pins to the newest available",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, parsed, err := loadStackReport(cmd, args[:1])
			if err != nil {
				return err
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			effectiveDigest := cfg.Bump.Digest
			if cmd.Flags().Changed("digest") {
				effectiveDigest = digest
			}

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

			var plans []plannedChange
			for _, img := range r.Images {
				if only != "" && img.Service != only {
					continue
				}
				svc, ok := byService[img.Service]
				if !ok {
					continue
				}
				st := parsed[0]
				if effectiveDigest && svc.Ref.Shape == imageref.ShapeDigestOnly {
					fmt.Fprintf(cmd.ErrOrStderr(), "notice: %s/%s is already digest-only; digest pinning leaves its shape unchanged\n", st.Name, svc.Name)
				}

				change, err := bump.PlanWithOptions(st, svc, img, bump.Options{Digest: effectiveDigest})
				if err != nil {
					// A refusal is informational, not fatal: other services
					// in the stack may still be bumpable. This includes a
					// candidate whose digest could not be resolved.
					fmt.Fprintln(cmd.ErrOrStderr(), "skipped:", err)
					continue
				}

				plans = append(plans, plannedChange{stack: st.Name, service: svc.Name, change: change})
			}

			if dryRun {
				for _, plan := range plans {
					d, err := bump.Diff(plan.change)
					if err != nil {
						return err
					}
					fmt.Fprint(cmd.OutOrStdout(), d)
				}
				return nil
			}
			changes := make([]bump.Change, 0, len(plans))
			for _, plan := range plans {
				changes = append(changes, plan.change)
			}
			if err := bump.ApplyAll(changes); err != nil {
				return err
			}
			for _, plan := range plans {
				fmt.Fprintf(cmd.OutOrStdout(), "bumped %s/%s: %s -> %s\n", plan.stack, plan.service, plan.change.Old, plan.change.New)
			}
			if len(plans) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "nothing to bump")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print a unified diff instead of writing")
	cmd.Flags().BoolVar(&digest, "digest", false, "pin resolved digests for eligible tagged pins, including current versions (overrides bump.digest)")
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
