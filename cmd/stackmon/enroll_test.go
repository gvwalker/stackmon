package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gvwalker/stackmon/internal/inventory"
)

func TestResolveReturnsEnrolledStack(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inv := inventory.Inventory{Stacks: []inventory.Stack{
		{Name: "traefik", Dir: dir, File: "compose.yaml"},
	}}

	got, missing, err := resolve(inv, []string{"traefik"})
	if err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want none", missing)
	}
	if len(got) != 1 || got[0].Name != "traefik" {
		t.Errorf("resolve = %+v", got)
	}
}

// A moved or unmounted stack must not abort the whole run: it degrades to a
// warning, exactly like an unparseable compose file does, and the run
// continues at exit 0.
func TestResolveReportsMissingStackPathAsWarningNotError(t *testing.T) {
	inv := inventory.Inventory{Stacks: []inventory.Stack{
		{Name: "ghost", Dir: filepath.Join(t.TempDir(), "gone"), File: "compose.yaml"},
	}}

	stacks, missing, err := resolve(inv, nil)
	if err != nil {
		t.Fatalf("resolve with a missing path = error %v, want nil (the run must continue)", err)
	}
	if len(stacks) != 0 {
		t.Errorf("stacks = %+v, want none (a missing stack must not be returned as present)", stacks)
	}
	if len(missing) != 1 || !strings.Contains(missing[0].Error(), "ghost") {
		t.Errorf("missing = %v, want exactly one warning naming ghost", missing)
	}
}

func TestResolveUnknownNameErrors(t *testing.T) {
	if _, _, err := resolve(inventory.Inventory{}, []string{"nope"}); err == nil {
		t.Fatal("resolve of an unenrolled name = nil error, want error")
	}
}
