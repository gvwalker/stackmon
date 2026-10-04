package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/gvwalker/stackmon/internal/compose"
	"github.com/gvwalker/stackmon/internal/local"
	"github.com/gvwalker/stackmon/internal/registry"
	"github.com/gvwalker/stackmon/internal/report"
)

// loadStackReport is the report-loading pipeline shared by check, show, and
// bump: load the inventory, resolve the requested stack names, parse and
// probe them, and print every warning (a stack whose path vanished, a
// compose file that failed to parse) to stderr before returning. Callers
// get back only the report and the parsed stacks that fed it; none of them
// need the missing/failure lists once printed.
//
// A stack that fails to parse does not abort the run: its failure is
// printed alongside the report built from the stacks that did parse, so one
// unparseable compose file never hides every other stack's status.
func loadStackReport(cmd *cobra.Command, names []string) (report.Report, []compose.Stack, error) {
	inv, _, err := loadInventory()
	if err != nil {
		return report.Report{}, nil, err
	}
	stacks, missing, err := resolve(inv, names)
	if err != nil {
		return report.Report{}, nil, err
	}

	cfg, err := loadConfig()
	if err != nil {
		return report.Report{}, nil, err
	}

	ctx := cmd.Context()
	var parsed []compose.Stack
	var failures []error
	type stackError struct {
		name string
		err  error
	}
	var failedStacks []stackError
	var serviceWarnings []stackError
	for _, s := range stacks {
		st, err := compose.Load(ctx, s)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", s.Name, err))
			failedStacks = append(failedStacks, stackError{name: s.Name, err: err})
			continue
		}
		for _, w := range st.Warnings {
			failures = append(failures, fmt.Errorf("%s: %w", s.Name, w))
			serviceWarnings = append(serviceWarnings, stackError{name: s.Name, err: w})
		}
		parsed = append(parsed, st)
	}

	r := report.Build(ctx, parsed, report.Options{
		Registry:    registry.New(),
		Docker:      local.New(),
		Config:      cfg,
		Concurrency: cfg.Concurrency,
	})

	// Printed to stderr first so stdout stays a clean table for piping,
	// regardless of caller (--json, detail view, or bump's diff). The same
	// problems are also carried in the report's Diagnostics: the stderr
	// lines are for a person watching, the Diagnostics are for a consumer
	// that cannot scrape stderr.
	var diag report.Diagnostics
	for _, m := range missing {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning:", m)
		diag.MissingStacks = append(diag.MissingStacks, report.StackFailure{Stack: m.name, Reason: fmt.Sprintf("no longer exists at %s", m.path)})
	}
	for _, f := range failures {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning:", f)
	}
	for _, f := range failedStacks {
		diag.ParseFailures = append(diag.ParseFailures, report.StackFailure{Stack: f.name, Reason: f.err.Error()})
	}
	for _, w := range serviceWarnings {
		diag.ServiceWarnings = append(diag.ServiceWarnings, report.StackFailure{Stack: w.name, Reason: w.err.Error()})
	}
	if r.DockerError != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning:", r.DockerError)
	}

	diag.StacksChecked = len(parsed)
	diag.StacksSkipped = len(missing) + len(failedStacks)
	diag.ServicesChecked = len(r.Images)
	diag.ServicesSkipped = len(serviceWarnings)
	r.Diagnostics = diag

	return r, parsed, nil
}
