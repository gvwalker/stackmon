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

// digestA, digestB, digestC are syntactically valid (64 hex char) fake
// sha256 digests. go-containerregistry's name.ParseReference validates
// digest length and rejects short placeholders like "sha256:aaaa", and
// Plan itself now validates the rewritten reference before returning it, so
// every fixture reference that ends up in a Change.New needs a full-length
// digest.
const digestA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

var digestB = strings.Repeat("b", 64)
var digestC = strings.Repeat("c", 64)
var sha256B = "sha256:" + digestB
var sha256C = "sha256:" + digestC

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
	img := report.Image{RegistryDigest: sha256B}

	c, err := Plan(st, svc, img)
	if err != nil {
		t.Fatalf("Plan error: %v", err)
	}
	if want := "traefik@" + sha256B; c.New != want {
		t.Fatalf("New = %q, want %q", c.New, want)
	}
	if err := Apply(c); err != nil {
		t.Fatalf("Apply error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(got)
	if !strings.Contains(out, "image: traefik@"+sha256B) {
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
	img := report.Image{Candidate: "v0.107.80", CandidateDigest: sha256C}

	c, err := Plan(st, svc, img)
	if err != nil {
		t.Fatalf("Plan error: %v", err)
	}
	if want := "adguard/adguardhome:v0.107.80@" + sha256C; c.New != want {
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

func TestPlanDigestPinsCurrentRecognizedVersion(t *testing.T) {
	const body = "services:\n  a:\n    image: example:v1.2.3\n"
	st, svc, _ := serviceAt(t, body, "example:v1.2.3")

	c, err := PlanWithOptions(st, svc, report.Image{RegistryDigest: sha256B}, Options{Digest: true})
	if err != nil {
		t.Fatalf("PlanWithOptions error: %v", err)
	}
	if want := "example:v1.2.3@" + sha256B; c.New != want {
		t.Errorf("New = %q, want %q", c.New, want)
	}
}

func TestPlanDigestDoesNotPinCurrentOpaqueTag(t *testing.T) {
	const body = "services:\n  a:\n    image: example:latest\n"
	st, svc, _ := serviceAt(t, body, "example:latest")

	_, err := PlanWithOptions(st, svc, report.Image{RegistryDigest: sha256B}, Options{Digest: true})
	if err == nil {
		t.Fatal("PlanWithOptions on an opaque tag = nil error, want refusal")
	}
}

func TestPlanDigestRefusesCurrentVersionWithoutDigest(t *testing.T) {
	const body = "services:\n  a:\n    image: example:1.2.3\n"
	st, svc, _ := serviceAt(t, body, "example:1.2.3")

	_, err := PlanWithOptions(st, svc, report.Image{}, Options{Digest: true})
	if err == nil {
		t.Fatal("PlanWithOptions without a declared tag digest = nil error, want refusal")
	}
	if !strings.Contains(err.Error(), "digest could not be resolved") {
		t.Errorf("error = %v, want digest lookup explanation", err)
	}
}

// A floating tag has nothing to advance to; pinning it silently would change
// the user's update policy behind their back.
func TestPlanRefusesFloatingReference(t *testing.T) {
	const body = "services:\n  a:\n    image: lscr.io/linuxserver/sonarr:latest\n"
	st, svc, _ := serviceAt(t, body, "lscr.io/linuxserver/sonarr:latest")

	_, err := Plan(st, svc, report.Image{RegistryDigest: sha256B})
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

// A digest-only pin's registry digest describes only the declared
// reference, never a different candidate version, but its CandidateDigest
// is resolved independently and is exactly what a digest-only pin needs to
// advance -- this is a legitimate bump, not a refusal.
func TestPlanAdvancesDigestOnlyPinToCandidateDigest(t *testing.T) {
	body := "services:\n  a:\n    image: traefik@sha256:" + digestA + "\n"
	st, svc, _ := serviceAt(t, body, "traefik@sha256:"+digestA)

	c, err := Plan(st, svc, report.Image{Version: "v3.0.0", Candidate: "v3.1.0", CandidateDigest: sha256C})
	if err != nil {
		t.Fatalf("Plan error: %v", err)
	}
	if want := "traefik@" + sha256C; c.New != want {
		t.Errorf("New = %q, want %q", c.New, want)
	}
}

// Without the candidate's own digest, a digest-only pin cannot be safely
// advanced: there is no tag to fall back to, and reusing RegistryDigest
// would silently pin the wrong version's bytes.
func TestPlanRefusesDigestOnlyBumpWhenCandidateDigestUnknown(t *testing.T) {
	body := "services:\n  a:\n    image: traefik@sha256:" + digestA + "\n"
	st, svc, _ := serviceAt(t, body, "traefik@sha256:"+digestA)

	_, err := Plan(st, svc, report.Image{Version: "v3.0.0", Candidate: "v3.1.0"})
	if err == nil {
		t.Fatal("Plan advancing a digest-only pin with no known candidate digest = nil error, want refusal")
	}
	if !strings.Contains(err.Error(), "v3.0.0") {
		t.Errorf("error should name the current version, got: %v", err)
	}
	if !strings.Contains(err.Error(), "v3.1.0") {
		t.Errorf("error should name the candidate version, got: %v", err)
	}
}

// Same guard for a tag+digest pin: without the candidate's own digest, the
// tag cannot be advanced while keeping the pin trustworthy.
func TestPlanRefusesTagDigestBumpWhenCandidateDigestUnknown(t *testing.T) {
	body := "services:\n  a:\n    image: adguard/adguardhome:v0.107.79@sha256:" + digestA + "\n"
	st, svc, _ := serviceAt(t, body, "adguard/adguardhome:v0.107.79@sha256:"+digestA)

	_, err := Plan(st, svc, report.Image{Candidate: "v0.107.80"})
	if err == nil {
		t.Fatal("Plan advancing a tag+digest pin with no known candidate digest = nil error, want refusal")
	}
	if !strings.Contains(err.Error(), "v0.107.79") {
		t.Errorf("error should name the current version, got: %v", err)
	}
	if !strings.Contains(err.Error(), "v0.107.80") {
		t.Errorf("error should name the candidate version, got: %v", err)
	}
}

// The guard is what makes bump safe to run after a check from minutes ago.
func TestApplyAbortsWhenFileChangedSinceCheck(t *testing.T) {
	st, svc, path := serviceAt(t, traefikFile, "traefik@sha256:"+digestA)
	c, err := Plan(st, svc, report.Image{RegistryDigest: sha256B})
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
	c, err := Plan(st, svc, report.Image{RegistryDigest: sha256B})
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
	c, err := Plan(st, svc, report.Image{RegistryDigest: sha256B})
	if err != nil {
		t.Fatal(err)
	}
	d, err := Diff(c)
	if err != nil {
		t.Fatalf("Diff error: %v", err)
	}
	// "-" and "+" alone are satisfied by the "---"/"+++" headers even if
	// the body lines were garbage, so assert the actual old and new content
	// lines instead.
	wantOld := "-    image: traefik@sha256:" + digestA
	wantNew := "+    image: traefik@" + sha256B
	if !strings.Contains(d, wantOld) {
		t.Errorf("diff missing old content line %q:\n%s", wantOld, d)
	}
	if !strings.Contains(d, wantNew) {
		t.Errorf("diff missing new content line %q:\n%s", wantNew, d)
	}
}

// RegistryDigest describes only the declared reference, never a different
// candidate version. A tag+digest bump that reused RegistryDigest for the
// new digest would silently pair the advancing tag with the *previous*
// version's digest -- a syntactically valid, factually wrong pin. This
// proves the candidate's own digest wins.
func TestPlanTagDigestBumpUsesCandidateDigestNotDeclaredDigest(t *testing.T) {
	body := "services:\n  a:\n    image: adguard/adguardhome:v0.107.79@sha256:" + digestA + "\n"
	st, svc, _ := serviceAt(t, body, "adguard/adguardhome:v0.107.79@sha256:"+digestA)
	img := report.Image{
		Candidate:       "v0.107.80",
		RegistryDigest:  "sha256:" + digestA, // the declared v0.107.79's own digest
		CandidateDigest: sha256C,             // v0.107.80's digest -- different
	}

	c, err := Plan(st, svc, img)
	if err != nil {
		t.Fatalf("Plan error: %v", err)
	}
	if want := "adguard/adguardhome:v0.107.80@" + sha256C; c.New != want {
		t.Errorf("New = %q, want %q (must use CandidateDigest, not RegistryDigest)", c.New, want)
	}
	if strings.Contains(c.New, digestA) {
		t.Errorf("New = %q pairs the new tag with the declared reference's digest instead of the candidate's", c.New)
	}
}

// Change's fields are exported and the type is explicitly designed to
// survive between a check and a later bump, so a hostile or stale value
// (here an inverted range, Offset 5 paired with Length -3) is in scope.
// Apply must reject it cleanly rather than let
// data[c.Offset:c.Offset+c.Length] panic with "slice bounds out of range".
func TestApplyRejectsNonPositiveLength(t *testing.T) {
	st, svc, path := serviceAt(t, traefikFile, "traefik@sha256:"+digestA)
	c, err := Plan(st, svc, report.Image{RegistryDigest: sha256B})
	if err != nil {
		t.Fatal(err)
	}
	c.Length = -3

	if err := Apply(c); err == nil {
		t.Fatal("Apply with a negative Length = nil error, want refusal")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != traefikFile {
		t.Error("Apply modified the file despite an invalid Length")
	}
}

// A zero Length would replace nothing while still claiming a rewrite
// happened; Plan never produces one, so it can only arrive via a
// hand-built or corrupted Change and must be refused the same way.
func TestApplyRejectsZeroLength(t *testing.T) {
	st, svc, _ := serviceAt(t, traefikFile, "traefik@sha256:"+digestA)
	c, err := Plan(st, svc, report.Image{RegistryDigest: sha256B})
	if err != nil {
		t.Fatal(err)
	}
	c.Length = 0

	if err := Apply(c); err == nil {
		t.Fatal("Apply with a zero Length = nil error, want refusal")
	}
}

// Diff must never panic on a hostile or stale Change either: data[:c.Offset]
// with a negative Offset panics immediately, before any length check runs.
func TestDiffRejectsNegativeOffset(t *testing.T) {
	st, svc, _ := serviceAt(t, traefikFile, "traefik@sha256:"+digestA)
	c, err := Plan(st, svc, report.Image{RegistryDigest: sha256B})
	if err != nil {
		t.Fatal(err)
	}
	c.Offset = -1

	if _, err := Diff(c); err == nil {
		t.Fatal("Diff with a negative Offset = nil error, want refusal")
	}
}

func TestDiffRejectsNonPositiveLength(t *testing.T) {
	st, svc, _ := serviceAt(t, traefikFile, "traefik@sha256:"+digestA)
	c, err := Plan(st, svc, report.Image{RegistryDigest: sha256B})
	if err != nil {
		t.Fatal(err)
	}
	c.Length = -3

	if _, err := Diff(c); err == nil {
		t.Fatal("Diff with a negative Length = nil error, want refusal")
	}
}

// A dry-run that lies about the pending change is worse than no dry-run.
// After a concurrent edit, Diff must refuse exactly like Apply does, rather
// than rendering a preview against text that no longer matches Old. The
// edit here is deliberately the same length as the original digest so the
// file's total size is unchanged: only a genuine content comparison (not
// merely Offset+Length exceeding len(data)) can catch it.
func TestDiffAbortsWhenFileChangedSinceCheck(t *testing.T) {
	st, svc, path := serviceAt(t, traefikFile, "traefik@sha256:"+digestA)
	c, err := Plan(st, svc, report.Image{RegistryDigest: sha256B})
	if err != nil {
		t.Fatal(err)
	}

	const digestE = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	edited := strings.Replace(traefikFile, "traefik@sha256:"+digestA, "traefik@sha256:"+digestE, 1)
	if len(edited) != len(traefikFile) {
		t.Fatalf("test fixture bug: edited length %d != original length %d", len(edited), len(traefikFile))
	}
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Diff(c); err == nil {
		t.Fatal("Diff after a same-length concurrent edit = nil error, want refusal")
	}
}

// This is the only code that writes to a live production compose file; it
// must be impossible to write a reference the tool cannot read back.
// RegistryDigest comes straight from an exported, unvalidated string field,
// so a syntactically invalid digest must be refused by Plan rather than
// written.
func TestPlanRefusesUnparseableRewrittenReference(t *testing.T) {
	st, svc, _ := serviceAt(t, traefikFile, "traefik@sha256:"+digestA)

	_, err := Plan(st, svc, report.Image{RegistryDigest: "sha256:not-a-valid-digest"})
	if err == nil {
		t.Fatal("Plan with an invalid digest = nil error, want refusal")
	}
}
