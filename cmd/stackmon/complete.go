package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/gvwalker/stackmon/internal/compose"
)

// completeEnrolledStacks returns enrolled stack names as completion candidates.
// Any stacks already listed in args are excluded so that multi-stack commands
// like check do not re-suggest already selected stacks.
func completeEnrolledStacks(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	inv, _, err := loadInventory()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	chosen := make(map[string]bool, len(args))
	for _, a := range args {
		chosen[a] = true
	}

	var comps []string
	for _, s := range inv.Stacks {
		if chosen[s.Name] {
			continue
		}
		comps = append(comps, fmt.Sprintf("%s\t%s", s.Name, s.Path()))
	}
	sort.Strings(comps)
	return comps, cobra.ShellCompDirectiveNoFileComp
}

// completeStackServices returns the names of services defined in the given stack.
// If the stack is not enrolled or its compose file cannot be read, it returns nil
// so shell completion degrades silently without error.
func completeStackServices(cmd *cobra.Command, stackName string, _ string) ([]string, cobra.ShellCompDirective) {
	inv, _, err := loadInventory()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	s, ok := inv.Find(stackName)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	st, err := compose.Load(ctx, s.Name, s.Dir, s.File)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var comps []string
	for _, svc := range st.Services {
		comps = append(comps, svc.Name)
	}
	sort.Strings(comps)
	return comps, cobra.ShellCompDirectiveNoFileComp
}
