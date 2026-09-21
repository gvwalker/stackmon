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
	return loadStackReportWithClients(cmd, names, registry.New(), local.New())
}

// loadStackReportWithClients keeps command tests on the production loading
// path while allowing deterministic registry and Docker probes.
func loadStackReportWithClients(cmd *cobra.Command, names []string, reg report.RegistryProber, docker local.Prober) (report.Report, []compose.Stack, error) {
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
	for _, s := range stacks {
		st, err := compose.Load(ctx, s.Name, s.Dir, s.File)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", s.Name, err))
			continue
		}
		for _, w := range st.Warnings {
			failures = append(failures, fmt.Errorf("%s: %w", s.Name, w))
		}
		parsed = append(parsed, st)
	}

	r := report.Build(ctx, parsed, report.Options{
		Registry:    reg,
		Docker:      docker,
		Config:      cfg,
		Concurrency: cfg.Concurrency,
	})

	// Printed to stderr first so stdout stays a clean table for piping,
	// regardless of caller (--json, detail view, or bump's diff).
	printFailures(cmd, missing)
	printFailures(cmd, failures)

	return r, parsed, nil
}

// printFailures surfaces per-stack parse failures on stderr with a
// "warning:" prefix, distinct from main.go's "error:" prefix on exit 1: a
// parse failure is not a stackmon failure, so grepping stderr must be able
// to tell the two apart without checking the exit code.
func printFailures(cmd *cobra.Command, failures []error) {
	for _, f := range failures {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning:", f)
	}
}
