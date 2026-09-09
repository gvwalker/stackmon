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

	got, err := resolve(inv, []string{"traefik"})
	if err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "traefik" {
		t.Errorf("resolve = %+v", got)
	}
}

// A moved or deleted stack must be a loud error, never a silent skip.
func TestResolveErrorsWhenStackPathMissing(t *testing.T) {
	inv := inventory.Inventory{Stacks: []inventory.Stack{
		{Name: "ghost", Dir: filepath.Join(t.TempDir(), "gone"), File: "compose.yaml"},
	}}

	_, err := resolve(inv, nil)
	if err == nil {
		t.Fatal("resolve with a missing path = nil error, want error")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should name the stack, got: %v", err)
	}
}

func TestResolveUnknownNameErrors(t *testing.T) {
	if _, err := resolve(inventory.Inventory{}, []string{"nope"}); err == nil {
		t.Fatal("resolve of an unenrolled name = nil error, want error")
	}
}
