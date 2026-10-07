package discover

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gvwalker/stackmon/internal/inventory"
	"github.com/gvwalker/stackmon/internal/local"
)

func TestDockerCandidates(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"compose.yaml", "prod.yml", "a.yml", "b.yml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("services: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name     string
		configs  string
		wantFile string
		wantOK   bool
	}{
		{"label present", "prod.yml", "prod.yml", true},
		{"label absolute", filepath.Join(dir, "prod.yml"), "prod.yml", true},
		{"label stale", "gone.yml", "compose.yaml", true},
		{"label multi-file", "a.yml,b.yml", "a.yml", true},
		{"label absent", "", "compose.yaml", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DockerCandidates([]local.Container{{
				Project:            "app",
				ProjectWorkingDir:  dir,
				ProjectConfigFiles: tc.configs,
			}}, inventory.Inventory{})
			if len(got) != 1 {
				t.Fatalf("got %d candidates, want 1", len(got))
			}
			if !tc.wantOK || got[0].File != tc.wantFile {
				t.Fatalf("file = %q, want %q", got[0].File, tc.wantFile)
			}
			if got[0].Dir != filepath.Clean(dir) || got[0].Project != "app" {
				t.Fatalf("candidate = %+v, want dir %q project app", got[0], filepath.Clean(dir))
			}
		})
	}
}

func TestDockerCandidatesNoComposeFile(t *testing.T) {
	dir := t.TempDir()
	got := DockerCandidates([]local.Container{{
		Project:            "app",
		ProjectWorkingDir:  dir,
		ProjectConfigFiles: "compose.yaml",
	}}, inventory.Inventory{})
	if len(got) != 0 {
		t.Fatalf("got %d candidates, want 0", len(got))
	}
}
