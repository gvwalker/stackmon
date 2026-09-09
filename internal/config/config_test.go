package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadReadsRootsAndOverrides(t *testing.T) {
	p := writeTemp(t, `
roots = ["/media/media-library/config", "/media/fast"]
concurrency = 4

[stacks.traefik.images."traefik"]
repo = "traefik/traefik"

[stacks.postgres.images."pgvector/pgvector"]
constraint = "pg18*"
`)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(c.Roots) != 2 || c.Roots[0] != "/media/media-library/config" {
		t.Errorf("Roots = %v", c.Roots)
	}
	if c.Concurrency != 4 {
		t.Errorf("Concurrency = %d, want 4", c.Concurrency)
	}
	if got := c.Image("traefik", "traefik").Repo; got != "traefik/traefik" {
		t.Errorf("traefik repo override = %q", got)
	}
	if got := c.Image("postgres", "pgvector/pgvector").Constraint; got != "pg18*" {
		t.Errorf("postgres constraint override = %q", got)
	}
}

func TestImageReturnsZeroWhenNoOverride(t *testing.T) {
	c := Config{}
	got := c.Image("nope", "nothing")
	if got.Repo != "" || got.Constraint != "" {
		t.Errorf("Image on empty config = %+v, want zero", got)
	}
}

func TestLoadDefaultsConcurrency(t *testing.T) {
	c, err := Load(writeTemp(t, `roots = ["/tmp"]`))
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if c.Concurrency != 8 {
		t.Errorf("Concurrency = %d, want default 8", c.Concurrency)
	}
}

func TestLoadMissingFileReturnsEmptyConfig(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("Load of missing file should not error, got: %v", err)
	}
	if len(c.Roots) != 0 {
		t.Errorf("Roots = %v, want empty", c.Roots)
	}
	if c.Concurrency != 8 {
		t.Errorf("Concurrency = %d, want default 8", c.Concurrency)
	}
}

func TestLoadRejectsMalformedTOML(t *testing.T) {
	if _, err := Load(writeTemp(t, `roots = [`)); err == nil {
		t.Fatal("Load of malformed TOML = nil error, want error")
	}
}

func TestLoadRejectsNegativeConcurrency(t *testing.T) {
	if _, err := Load(writeTemp(t, "concurrency = -1")); err == nil {
		t.Fatal("Load with negative concurrency = nil error, want error")
	}
}

func TestLoadRejectsZeroConcurrency(t *testing.T) {
	if _, err := Load(writeTemp(t, "concurrency = 0")); err == nil {
		t.Fatal("Load with explicit zero concurrency = nil error, want error")
	}
}
