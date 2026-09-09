package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gvwalker/stackmon/internal/inventory"
)

// An enrolled stack whose directory has vanished (an unmounted drive, a
// moved directory) must not abort the whole run: check degrades to a
// stderr warning and continues at exit 0, the same contract an unparseable
// compose file already gets.
func TestCheckReportsMissingStackPathWithoutError(t *testing.T) {
	invPath := filepath.Join(t.TempDir(), "inventory.json")
	inv := inventory.Inventory{Stacks: []inventory.Stack{
		{Name: "ghost", Dir: filepath.Join(t.TempDir(), "gone"), File: "compose.yaml"},
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

	cmd := newCheckCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(nil)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("check returned an error for a missing stack path, want nil: %v", err)
	}
	if !strings.Contains(stderr.String(), "ghost") {
		t.Errorf("stderr should name the stack, got: %q", stderr.String())
	}
}
