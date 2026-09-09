package inventory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveThenLoadRoundTrips(t *testing.T) {
	p := filepath.Join(t.TempDir(), "inventory.json")
	when := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	in := Inventory{Version: 1, Stacks: []Stack{
		{Name: "traefik", Dir: "/media/media-library/config/traefik", File: "docker-compose.yml", Enrolled: when},
	}}

	if err := Save(p, in); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(got.Stacks) != 1 || got.Stacks[0].Name != "traefik" {
		t.Fatalf("Stacks = %+v", got.Stacks)
	}
	if !got.Stacks[0].Enrolled.Equal(when) {
		t.Errorf("Enrolled = %v, want %v", got.Stacks[0].Enrolled, when)
	}
}

func TestLoadMissingFileIsEmptyInventory(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("Load of missing file should not error, got: %v", err)
	}
	if len(got.Stacks) != 0 {
		t.Errorf("Stacks = %+v, want empty", got.Stacks)
	}
	if got.Version != CurrentVersion {
		t.Errorf("Version = %d, want %d", got.Version, CurrentVersion)
	}
}

func TestAddRejectsDuplicateName(t *testing.T) {
	inv := Inventory{Version: CurrentVersion}
	if err := inv.Add(Stack{Name: "traefik", Dir: "/a/traefik", File: "compose.yaml"}); err != nil {
		t.Fatalf("first Add error: %v", err)
	}
	err := inv.Add(Stack{Name: "traefik", Dir: "/b/traefik", File: "compose.yaml"})
	if err == nil {
		t.Fatal("duplicate Add = nil error, want error")
	}
	if len(inv.Stacks) != 1 {
		t.Errorf("Stacks = %d, want 1", len(inv.Stacks))
	}
}

func TestRemoveUnknownNameErrors(t *testing.T) {
	inv := Inventory{Version: CurrentVersion}
	if err := inv.Remove("ghost"); err == nil {
		t.Fatal("Remove of unknown name = nil error, want error")
	}
}

func TestRemoveDeletesTheStack(t *testing.T) {
	inv := Inventory{Version: CurrentVersion}
	_ = inv.Add(Stack{Name: "a", Dir: "/x/a", File: "compose.yaml"})
	_ = inv.Add(Stack{Name: "b", Dir: "/x/b", File: "compose.yaml"})
	if err := inv.Remove("a"); err != nil {
		t.Fatalf("Remove error: %v", err)
	}
	if _, ok := inv.Find("a"); ok {
		t.Error("Find(a) still reports enrolled after Remove")
	}
	if _, ok := inv.Find("b"); !ok {
		t.Error("Remove(a) also removed b")
	}
}

func TestStackPathJoinsDirAndFile(t *testing.T) {
	s := Stack{Dir: "/media/fast/adguard", File: "compose.yaml"}
	if got, want := s.Path(), "/media/fast/adguard/compose.yaml"; got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestSaveLeavesNoTempFileBehind(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "inventory.json")
	if err := Save(p, Inventory{Version: CurrentVersion}); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want only inventory.json", len(entries))
	}
}
