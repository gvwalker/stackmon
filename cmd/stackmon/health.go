package main

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/gvwalker/stackmon/internal/compose"
	"github.com/gvwalker/stackmon/internal/health"
	"github.com/gvwalker/stackmon/internal/local"
	"github.com/gvwalker/stackmon/internal/render"
)

func newHealthCmd() *cobra.Command {
	var asJSON, compact, failOnUnhealthy bool

	cmd := &cobra.Command{
		Use:   "health [stack...]",
		Short: "Report runtime health of every service in the enrolled stacks",
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := loadHealthReport(cmd, args)
			if err != nil {
				return err
			}

			switch {
			case compact:
				if err := render.CompactHealthJSON(cmd.OutOrStdout(), r); err != nil {
					return err
				}
			case asJSON:
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetEscapeHTML(false)
				enc.SetIndent("", "  ")
				if err := enc.Encode(r); err != nil {
					return err
				}
			default:
				if err := render.HealthTable(cmd.OutOrStdout(), r); err != nil {
					return err
				}
			}

			if failOnUnhealthy && r.Unhealthy() {
				return errUnhealthy
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the full runtime health report as JSON")
	cmd.Flags().BoolVarP(&compact, "compact", "c", false, "emit JSON carrying only the actionable health fields")
	cmd.Flags().BoolVar(&failOnUnhealthy, "fail-on-unhealthy", false, "exit 2 when any service is not running, unhealthy, starting, unknown, or incomplete")
	cmd.ValidArgsFunction = completeEnrolledStacks
	return cmd
}

// loadHealthReport resolves enrolled stacks and queries local Docker runtime
// health directly without contacting any container registry.
func loadHealthReport(cmd *cobra.Command, names []string) (health.Report, error) {
	if _, err := loadConfig(); err != nil {
		return health.Report{}, err
	}
	inv, _, err := loadInventory()
	if err != nil {
		return health.Report{}, err
	}
	stacks, missing, err := resolve(inv, names)
	if err != nil {
		return health.Report{}, err
	}

	ctx := cmd.Context()
	var parsed []compose.Stack
	var stackFailures []health.StackFailure
	var warnings []error

	for _, m := range missing {
		warnings = append(warnings, m)
		stackFailures = append(stackFailures, health.StackFailure{
			Stack:  m.name,
			Reason: fmt.Sprintf("no longer exists at %s", m.path),
		})
	}
	for _, s := range stacks {
		st, err := compose.LoadServices(ctx, s)
		if err != nil {
			warnings = append(warnings, fmt.Errorf("%s: %w", s.Name, err))
			stackFailures = append(stackFailures, health.StackFailure{
				Stack:  s.Name,
				Reason: err.Error(),
			})
			continue
		}
		parsed = append(parsed, st)
	}

	r := health.Build(ctx, parsed, health.Options{Docker: local.New()})

	sort.Slice(stackFailures, func(i, j int) bool {
		if stackFailures[i].Stack != stackFailures[j].Stack {
			return stackFailures[i].Stack < stackFailures[j].Stack
		}
		return stackFailures[i].Reason < stackFailures[j].Reason
	})
	if len(stackFailures) > 0 {
		r.StackFailures = stackFailures
	}

	for _, w := range warnings {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning:", w)
	}
	if r.DockerError != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning:", r.DockerError)
	}

	return r, nil
}
