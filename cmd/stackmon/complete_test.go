package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/gvwalker/stackmon/internal/inventory"
)

func setupTestStacks(t *testing.T) (invPath string) {
	t.Helper()

	dirAlpha := t.TempDir()
	composeAlpha := `
services:
  web:
    image: nginx:latest
  db:
    image: postgres:15
`
	if err := os.WriteFile(filepath.Join(dirAlpha, "compose.yaml"), []byte(composeAlpha), 0o644); err != nil {
		t.Fatal(err)
	}

	dirBeta := t.TempDir()
	composeBeta := `
services:
  app:
    image: node:18
`
	if err := os.WriteFile(filepath.Join(dirBeta, "docker-compose.yml"), []byte(composeBeta), 0o644); err != nil {
		t.Fatal(err)
	}

	invPath = filepath.Join(t.TempDir(), "inventory.json")
	inv := inventory.Inventory{Stacks: []inventory.Stack{
		{Name: "alpha", Dir: dirAlpha, File: "compose.yaml"},
		{Name: "beta", Dir: dirBeta, File: "docker-compose.yml"},
	}}
	if err := inventory.Save(invPath, inv); err != nil {
		t.Fatal(err)
	}

	return invPath
}

func TestCompleteBump(t *testing.T) {
	invPath := setupTestStacks(t)

	oldInv := flagInventory
	flagInventory = invPath
	t.Cleanup(func() { flagInventory = oldInv })

	cmd := newBumpCmd()
	if cmd.ValidArgsFunction == nil {
		t.Fatal("newBumpCmd().ValidArgsFunction is nil")
	}

	// 1st arg: completing <stack>
	comps, directive := cmd.ValidArgsFunction(cmd, []string{}, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want ShellCompDirectiveNoFileComp", directive)
	}
	if len(comps) != 2 {
		t.Fatalf("got %d completions, want 2: %v", len(comps), comps)
	}
	if !strings.HasPrefix(comps[0], "alpha") || !strings.HasPrefix(comps[1], "beta") {
		t.Errorf("completions = %v, want alpha and beta", comps)
	}

	// 2nd arg: completing [service] for stack "alpha"
	comps, directive = cmd.ValidArgsFunction(cmd, []string{"alpha"}, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want ShellCompDirectiveNoFileComp", directive)
	}
	if len(comps) != 2 {
		t.Fatalf("got %d completions, want 2 (db, web): %v", len(comps), comps)
	}
	if comps[0] != "db" || comps[1] != "web" {
		t.Errorf("completions = %v, want [db, web]", comps)
	}

	// 3rd arg: no further positional args
	comps, directive = cmd.ValidArgsFunction(cmd, []string{"alpha", "web"}, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want ShellCompDirectiveNoFileComp", directive)
	}
	if len(comps) != 0 {
		t.Errorf("got %d completions, want 0: %v", len(comps), comps)
	}
}

func TestCompleteBumpUnknownStack(t *testing.T) {
	invPath := setupTestStacks(t)

	oldInv := flagInventory
	flagInventory = invPath
	t.Cleanup(func() { flagInventory = oldInv })

	cmd := newBumpCmd()
	if cmd.ValidArgsFunction == nil {
		t.Fatal("newBumpCmd().ValidArgsFunction is nil")
	}

	comps, directive := cmd.ValidArgsFunction(cmd, []string{"nonexistent"}, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want ShellCompDirectiveNoFileComp", directive)
	}
	if len(comps) != 0 {
		t.Errorf("got %d completions for nonexistent stack, want 0", len(comps))
	}
}

func TestCompleteShow(t *testing.T) {
	invPath := setupTestStacks(t)

	oldInv := flagInventory
	flagInventory = invPath
	t.Cleanup(func() { flagInventory = oldInv })

	cmd := newShowCmd()
	if cmd.ValidArgsFunction == nil {
		t.Fatal("newShowCmd().ValidArgsFunction is nil")
	}

	comps, directive := cmd.ValidArgsFunction(cmd, []string{}, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want ShellCompDirectiveNoFileComp", directive)
	}
	if len(comps) != 2 {
		t.Fatalf("got %d completions, want 2: %v", len(comps), comps)
	}

	// Arg 2 should be empty
	comps, _ = cmd.ValidArgsFunction(cmd, []string{"alpha"}, "")
	if len(comps) != 0 {
		t.Errorf("got %d completions for 2nd arg of show, want 0", len(comps))
	}
}

func TestCompleteCheck(t *testing.T) {
	invPath := setupTestStacks(t)

	oldInv := flagInventory
	flagInventory = invPath
	t.Cleanup(func() { flagInventory = oldInv })

	cmd := newCheckCmd()
	if cmd.ValidArgsFunction == nil {
		t.Fatal("newCheckCmd().ValidArgsFunction is nil")
	}

	comps, directive := cmd.ValidArgsFunction(cmd, []string{}, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want ShellCompDirectiveNoFileComp", directive)
	}
	if len(comps) != 2 {
		t.Fatalf("got %d completions, want 2: %v", len(comps), comps)
	}

	// When "alpha" is already specified, it should only complete "beta"
	comps, _ = cmd.ValidArgsFunction(cmd, []string{"alpha"}, "")
	if len(comps) != 1 || !strings.HasPrefix(comps[0], "beta") {
		t.Errorf("got %v, want only [beta...]", comps)
	}
}

func TestCompleteUnenroll(t *testing.T) {
	invPath := setupTestStacks(t)

	oldInv := flagInventory
	flagInventory = invPath
	t.Cleanup(func() { flagInventory = oldInv })

	cmd := newUnenrollCmd()
	if cmd.ValidArgsFunction == nil {
		t.Fatal("newUnenrollCmd().ValidArgsFunction is nil")
	}

	comps, directive := cmd.ValidArgsFunction(cmd, []string{}, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want ShellCompDirectiveNoFileComp", directive)
	}
	if len(comps) != 2 {
		t.Fatalf("got %d completions, want 2: %v", len(comps), comps)
	}
}

func TestCompleteMissingInventory(t *testing.T) {
	oldInv := flagInventory
	flagInventory = filepath.Join(t.TempDir(), "nonexistent.json")
	t.Cleanup(func() { flagInventory = oldInv })

	cmd := newBumpCmd()
	if cmd.ValidArgsFunction == nil {
		t.Fatal("newBumpCmd().ValidArgsFunction is nil")
	}

	comps, directive := cmd.ValidArgsFunction(cmd, []string{}, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want ShellCompDirectiveNoFileComp", directive)
	}
	if len(comps) != 0 {
		t.Errorf("got %d completions, want 0 on missing inventory: %v", len(comps), comps)
	}
}

func TestCompleteRootCmdEndToEnd(t *testing.T) {
	invPath := setupTestStacks(t)

	oldInv := flagInventory
	flagInventory = invPath
	t.Cleanup(func() { flagInventory = oldInv })

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"__complete", "--inventory", invPath, "bump", ""})
	if err := root.Execute(); err != nil {
		t.Fatalf("__complete bump failed: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "alpha") || !strings.Contains(output, "beta") {
		t.Errorf("output missing stacks: %q", output)
	}

	// End-to-end 2nd arg: completing services for stack "alpha"
	var outServices bytes.Buffer
	root = newRootCmd()
	root.SetOut(&outServices)
	root.SetArgs([]string{"__complete", "--inventory", invPath, "bump", "alpha", ""})
	if err := root.Execute(); err != nil {
		t.Fatalf("__complete bump alpha failed: %v", err)
	}
	serviceOutput := outServices.String()
	if !strings.Contains(serviceOutput, "db") || !strings.Contains(serviceOutput, "web") {
		t.Errorf("output missing services: %q", serviceOutput)
	}
}
