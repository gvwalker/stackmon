package discover

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gvwalker/stackmon/internal/inventory"
	"github.com/gvwalker/stackmon/internal/local"
)

// tree creates the given relative paths as empty files under a temp dir.
func tree(t *testing.T, paths ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range paths {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("services: {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestScanFindsBothComposeNamings(t *testing.T) {
	root := tree(t, "traefik/docker-compose.yml", "adguard/compose.yaml")

	got, err := Scan([]string{root}, inventory.Inventory{})
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("found %d candidates, want 2: %+v", len(got), got)
	}
	// Sorted by name: adguard before traefik.
	if got[0].Name != "adguard" || got[0].File != "compose.yaml" {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].Name != "traefik" || got[1].File != "docker-compose.yml" {
		t.Errorf("got[1] = %+v", got[1])
	}
}

func TestScanMarksEnrolledCandidates(t *testing.T) {
	root := tree(t, "traefik/docker-compose.yml", "adguard/compose.yaml")
	inv := inventory.Inventory{Stacks: []inventory.Stack{
		{Name: "traefik", Dir: filepath.Join(root, "traefik"), File: "docker-compose.yml"},
	}}

	got, err := Scan([]string{root}, inv)
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	byName := map[string]Candidate{}
	for _, c := range got {
		byName[c.Name] = c
	}
	if !byName["traefik"].Enrolled {
		t.Error("traefik should be marked enrolled")
	}
	if byName["adguard"].Enrolled {
		t.Error("adguard should not be marked enrolled")
	}
}

func TestFindComposeFilePrefersComposeYaml(t *testing.T) {
	root := tree(t, "both/compose.yaml", "both/docker-compose.yml")

	got, ok := FindComposeFile(filepath.Join(root, "both"))
	if !ok {
		t.Fatal("FindComposeFile found nothing")
	}
	if got != "compose.yaml" {
		t.Errorf("FindComposeFile = %q, want compose.yaml (Compose's own precedence)", got)
	}
}

func TestScanSkipsDotDirectories(t *testing.T) {
	root := tree(t, ".hidden/compose.yaml", "visible/compose.yaml")

	got, err := Scan([]string{root}, inventory.Inventory{})
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "visible" {
		t.Errorf("Scan = %+v, want only visible", got)
	}
}

func TestScanStopsAtDepthThree(t *testing.T) {
	root := tree(t, "a/b/c/compose.yaml", "a/b/c/d/compose.yaml")

	got, err := Scan([]string{root}, inventory.Inventory{})
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "c" {
		t.Errorf("Scan = %+v, want only the depth-3 candidate", got)
	}
}

func TestScanIgnoresMissingRoot(t *testing.T) {
	got, err := Scan([]string{filepath.Join(t.TempDir(), "absent")}, inventory.Inventory{})
	if err != nil {
		t.Fatalf("a missing root should be skipped, not fatal; got: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Scan = %+v, want empty", got)
	}
}
func TestScanNormalisesEnrolledDirWithTrailingSeparator(t *testing.T) {
	root := tree(t, "stack/compose.yaml")
	stackDir := filepath.Join(root, "stack")
	// Enrol with a trailing separator to test normalisation
	inv := inventory.Inventory{Stacks: []inventory.Stack{
		{Name: "stack", Dir: stackDir + string(filepath.Separator), File: "compose.yaml"},
	}}

	got, err := Scan([]string{root}, inv)
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("found %d candidates, want 1: %+v", len(got), got)
	}
	if !got[0].Enrolled {
		t.Error("stack with trailing separator in Stack.Dir should be marked enrolled")
	}
}

// A compose file sitting directly in a configured root must be a
// candidate, since `enroll <path>` already accepts such a directory
// happily: the two commands must agree on what a stack is.
func TestScanFindsComposeFileDirectlyInRoot(t *testing.T) {
	root := tree(t, "compose.yaml")

	got, err := Scan([]string{root}, inventory.Inventory{})
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("found %d candidates, want 1: %+v", len(got), got)
	}
	if got[0].Dir != root {
		t.Errorf("Dir = %q, want the root itself %q", got[0].Dir, root)
	}
}

func TestDockerCandidatesFindsRunningComposeStack(t *testing.T) {
	root := tree(t, "media/compose.yaml")
	dir := filepath.Join(root, "media")
	containers := []local.Container{
		{Project: "media", ProjectWorkingDir: dir, ProjectConfigFiles: filepath.Join(dir, "compose.yaml"), Service: "web"},
		{Project: "media", ProjectWorkingDir: dir, ProjectConfigFiles: filepath.Join(dir, "compose.yaml"), Service: "db"},
	}

	got := DockerCandidates(containers, inventory.Inventory{})
	if len(got) != 1 {
		t.Fatalf("DockerCandidates = %d candidates, want 1: %+v", len(got), got)
	}
	if got[0].Name != "media" || got[0].Dir != dir || got[0].File != "compose.yaml" {
		t.Errorf("DockerCandidates = %+v", got[0])
	}
	if got[0].Project != "media" {
		t.Errorf("Project = %q, want media", got[0].Project)
	}
}

func TestMergeDeduplicatesRootAndDockerCandidates(t *testing.T) {
	root := tree(t, "media/compose.yaml")
	dir := filepath.Join(root, "media")
	rootCandidates, err := Scan([]string{root}, inventory.Inventory{})
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	dockerCandidates := DockerCandidates([]local.Container{
		{Project: "media", ProjectWorkingDir: dir, ProjectConfigFiles: filepath.Join(dir, "compose.yaml"), Service: "web"},
	}, inventory.Inventory{})

	got := Merge(inventory.Inventory{}, rootCandidates, dockerCandidates)
	if len(got) != 1 {
		t.Fatalf("Merge = %d candidates, want 1: %+v", len(got), got)
	}
	if got[0].Dir != dir || got[0].File != "compose.yaml" {
		t.Errorf("Merge = %+v", got[0])
	}
}

func TestDockerCandidatesIgnoresIncompleteProjects(t *testing.T) {
	dir := t.TempDir()
	tests := []local.Container{
		{Project: "relative", ProjectWorkingDir: "relative", ProjectConfigFiles: "relative/compose.yaml", Service: "web"},
		{Project: "missing-dir", ProjectWorkingDir: filepath.Join(dir, "missing"), ProjectConfigFiles: filepath.Join(dir, "missing", "compose.yaml"), Service: "web"},
		{Project: "missing-label", ProjectConfigFiles: filepath.Join(dir, "compose.yaml"), Service: "web"},
	}

	if got := DockerCandidates(tests, inventory.Inventory{}); len(got) != 0 {
		t.Errorf("DockerCandidates = %+v, want no candidates", got)
	}
}
