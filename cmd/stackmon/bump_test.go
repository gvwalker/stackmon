package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gvwalker/stackmon/internal/inventory"
)

// A stack whose compose file fails to parse must not turn bump into a
// stackmon failure: the same condition is a non-fatal warning in check and
// show, and bump must read the same way so a script cannot see a different
// exit code depending on which subcommand it ran.
func TestBumpReportsUnparseableStackWithoutError(t *testing.T) {
	dir := t.TempDir()
	// A required interpolation variable with no default and nothing to
	// supply it: the same failure class as the real sandbox stack
	// ("required variable ... is missing a value").
	compose := "services:\n  api:\n    image: ${REQUIRED_IMAGE:?must be set}\n"
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}

	invPath := filepath.Join(t.TempDir(), "inventory.json")
	inv := inventory.Inventory{Stacks: []inventory.Stack{
		{Name: "broken", Dir: dir, File: "compose.yaml"},
	}}
	if err := inventory.Save(invPath, inv); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	oldInv, oldCfg := flagInventory, flagConfig
	flagInventory, flagConfig = invPath, cfgPath
	t.Cleanup(func() { flagInventory, flagConfig = oldInv, oldCfg })

	cmd := newBumpCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"broken", "--dry-run"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("bump returned an error for an unparseable stack, want nil: %v", err)
	}
	if !strings.Contains(stderr.String(), "broken") {
		t.Errorf("stderr should name the stack, got: %q", stderr.String())
	}
}
