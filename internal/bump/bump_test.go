package bump

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gvwalker/stackmon/internal/compose"
	"github.com/gvwalker/stackmon/internal/imageref"
	"github.com/gvwalker/stackmon/internal/report"
)

// digestA is a syntactically valid (64 hex char) fake sha256 digest.
// go-containerregistry's name.ParseReference validates digest length and
// rejects short placeholders like "sha256:aaaa", so any fixture reference
// that flows through imageref.Parse needs a full-length digest. Other short
// "sha256:bbbb"-style values below are only ever compared or embedded
// verbatim (report.Image.RegistryDigest, the simulated concurrent edit) and
// are never parsed, so they can stay short.
const digestA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

const traefikFile = `services:
  traefik:
    # Pinned by digest deliberately: a bad traefik takes everything down.
    image: traefik@sha256:` + digestA + `
    restart: unless-stopped
`

func fixture(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "docker-compose.yml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func serviceAt(t *testing.T, body, raw string) (compose.Stack, compose.Service, string) {
	t.Helper()
	path := fixture(t, body)
	offset := strings.Index(body, raw)
	if offset < 0 {
		t.Fatalf("fixture does not contain %q", raw)
	}
	ref, err := imageref.Parse(raw, raw)
	if err != nil {
		t.Fatal(err)
	}
	st := compose.Stack{Name: "traefik", Dir: filepath.Dir(path), File: filepath.Base(path)}
	svc := compose.Service{Name: "traefik", Ref: ref, Offset: offset, Length: len(raw)}
	return st, svc, path
}

func TestApplyReplacesDigestAndPreservesComments(t *testing.T) {
	st, svc, path := serviceAt(t, traefikFile, "traefik@sha256:"+digestA)
	img := report.Image{RegistryDigest: "sha256:bbbb"}

	c, err := Plan(st, svc, img)
	if err != nil {
		t.Fatalf("Plan error: %v", err)
	}
	if c.New != "traefik@sha256:bbbb" {
		t.Fatalf("New = %q, want traefik@sha256:bbbb", c.New)
	}
	if err := Apply(c); err != nil {
		t.Fatalf("Apply error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(got)
	if !strings.Contains(out, "image: traefik@sha256:bbbb") {
		t.Errorf("digest not replaced:\n%s", out)
	}
	if !strings.Contains(out, "# Pinned by digest deliberately") {
		t.Errorf("comment was lost:\n%s", out)
	}
	if !strings.Contains(out, "restart: unless-stopped") {
		t.Errorf("unrelated content was lost:\n%s", out)
	}
}

func TestPlanAdvancesTagAndDigestTogether(t *testing.T) {
	body := "services:\n  a:\n    image: adguard/adguardhome:v0.107.79@sha256:" + digestA + "\n"
	st, svc, _ := serviceAt(t, body, "adguard/adguardhome:v0.107.79@sha256:"+digestA)
	img := report.Image{Candidate: "v0.107.80", RegistryDigest: "sha256:cccc"}

	c, err := Plan(st, svc, img)
	if err != nil {
		t.Fatalf("Plan error: %v", err)
	}
	if want := "adguard/adguardhome:v0.107.80@sha256:cccc"; c.New != want {
		t.Errorf("New = %q, want %q", c.New, want)
	}
}

func TestPlanAdvancesTagOnly(t *testing.T) {
	const body = "services:\n  a:\n    image: ghcr.io/vectorize-io/hindsight:0.9.1\n"
	st, svc, _ := serviceAt(t, body, "ghcr.io/vectorize-io/hindsight:0.9.1")

	c, err := Plan(st, svc, report.Image{Candidate: "1.0.0"})
	if err != nil {
		t.Fatalf("Plan error: %v", err)
	}
	if want := "ghcr.io/vectorize-io/hindsight:1.0.0"; c.New != want {
		t.Errorf("New = %q, want %q", c.New, want)
	}
}

// A floating tag has nothing to advance to; pinning it silently would change
// the user's update policy behind their back.
func TestPlanRefusesFloatingReference(t *testing.T) {
	const body = "services:\n  a:\n    image: lscr.io/linuxserver/sonarr:latest\n"
	st, svc, _ := serviceAt(t, body, "lscr.io/linuxserver/sonarr:latest")

	_, err := Plan(st, svc, report.Image{RegistryDigest: "sha256:dddd"})
	if err == nil {
		t.Fatal("Plan on a floating tag = nil error, want refusal")
	}
	if !strings.Contains(err.Error(), "latest") {
		t.Errorf("error should name the tag, got: %v", err)
	}
}

// The pin lives in .env, so rewriting the compose file would hard-code a
// value the author deliberately made configurable.
func TestPlanRefusesInterpolatedReference(t *testing.T) {
	const raw = "ghcr.io/karakeep-app/karakeep:${KARAKEEP_VERSION:-release}"
	const body = "services:\n  web:\n    image: " + raw + "\n"
	path := fixture(t, body)
	ref, err := imageref.Parse(raw, "ghcr.io/karakeep-app/karakeep:release")
	if err != nil {
		t.Fatal(err)
	}
	st := compose.Stack{Name: "karakeep", Dir: filepath.Dir(path), File: filepath.Base(path)}
	svc := compose.Service{Name: "web", Ref: ref, Offset: strings.Index(body, raw), Length: len(raw)}

	_, err = Plan(st, svc, report.Image{Candidate: "1.0.0"})
	if err == nil {
		t.Fatal("Plan on an interpolated ref = nil error, want refusal")
	}
	if !strings.Contains(err.Error(), "KARAKEEP_VERSION") {
		t.Errorf("error should name the variable, got: %v", err)
	}
}

func TestPlanRefusesWhenNothingToChange(t *testing.T) {
	body := "services:\n  a:\n    image: traefik@sha256:" + digestA + "\n"
	st, svc, _ := serviceAt(t, body, "traefik@sha256:"+digestA)

	if _, err := Plan(st, svc, report.Image{RegistryDigest: "sha256:" + digestA}); err == nil {
		t.Fatal("Plan with an identical digest = nil error, want refusal")
	}
}

// RULING E: a digest-only pin's registry digest describes only the declared
// reference (the registry serves exactly what was asked for), never the
// candidate version's digest. Plan cannot know what digest the candidate
// resolves to, so it must refuse rather than silently doing nothing or
// producing a confusing "already at X" message.
func TestPlanRefusesVersionBumpOnDigestOnlyPin(t *testing.T) {
	body := "services:\n  a:\n    image: traefik@sha256:" + digestA + "\n"
	st, svc, _ := serviceAt(t, body, "traefik@sha256:"+digestA)

	_, err := Plan(st, svc, report.Image{Version: "v3.0.0", Candidate: "v3.1.0"})
	if err == nil {
		t.Fatal("Plan advancing a digest-only pin's version = nil error, want refusal")
	}
	if !strings.Contains(err.Error(), "v3.0.0") {
		t.Errorf("error should name the current version, got: %v", err)
	}
	if !strings.Contains(err.Error(), "v3.1.0") {
		t.Errorf("error should name the candidate version, got: %v", err)
	}
	if !strings.Contains(err.Error(), "tag") {
		t.Errorf("error should explain moving the pin to a tag, got: %v", err)
	}
}

// The guard is what makes bump safe to run after a check from minutes ago.
func TestApplyAbortsWhenFileChangedSinceCheck(t *testing.T) {
	st, svc, path := serviceAt(t, traefikFile, "traefik@sha256:"+digestA)
	c, err := Plan(st, svc, report.Image{RegistryDigest: "sha256:bbbb"})
	if err != nil {
		t.Fatal(err)
	}

	// Someone edits the file between check and bump.
	edited := strings.Replace(traefikFile, "traefik@sha256:"+digestA, "traefik@sha256:9999", 1)
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Apply(c); err == nil {
		t.Fatal("Apply after a concurrent edit = nil error, want refusal")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != edited {
		t.Errorf("Apply modified the file despite refusing; got:\n%s\nwant (untouched):\n%s", got, edited)
	}
	// No leftover temp file: Apply's guard must fail before any write, not
	// merely after one.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory has %d entries, want exactly the compose file (no stray temp file): %v", len(entries), entries)
	}
}

func TestApplyPreservesFileMode(t *testing.T) {
	st, svc, path := serviceAt(t, traefikFile, "traefik@sha256:"+digestA)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Plan(st, svc, report.Image{RegistryDigest: "sha256:bbbb"})
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(c); err != nil {
		t.Fatal(err)
	}
	st2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st2.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st2.Mode().Perm())
	}
}

func TestDiffShowsBothLines(t *testing.T) {
	st, svc, _ := serviceAt(t, traefikFile, "traefik@sha256:"+digestA)
	c, err := Plan(st, svc, report.Image{RegistryDigest: "sha256:bbbb"})
	if err != nil {
		t.Fatal(err)
	}
	d, err := Diff(c)
	if err != nil {
		t.Fatalf("Diff error: %v", err)
	}
	if !strings.Contains(d, "-") || !strings.Contains(d, "+") {
		t.Errorf("diff lacks -/+ lines:\n%s", d)
	}
	if !strings.Contains(d, "sha256:bbbb") {
		t.Errorf("diff omits the new digest:\n%s", d)
	}
}
