package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bump is the only command that writes to a live compose file, so every
// scenario here ends by reading the file back: a rewrite that is not exactly
// the intended line is a corrupted user's stack.
func TestBumpAdvancesTagsAndLeavesTheFileAlone(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	e.push("app", "1.2.0", image{})
	body := fmt.Sprintf("services:\n"+
		"  # Pinned deliberately: a bad api takes the stack down.\n"+
		"  api:\n"+
		"    image: %s # keep this comment\n"+
		"    restart: unless-stopped\n"+
		"  worker:\n"+
		"    image: \"%s\"\n",
		e.ref("app", "1.0.0"), e.ref("app", "1.0.0"))
	path := e.enrollStack("demo", body)
	e.run("check", "--fail-on-update")

	got := e.run("bump", "demo")
	assertCode(t, "bump", got.code, 0, got)
	assertContains(t, "stdout", got.stdout, "bumped demo/api:", "bumped demo/worker:")

	assertEq(t, "compose file", e.read(path), fmt.Sprintf("services:\n"+
		"  # Pinned deliberately: a bad api takes the stack down.\n"+
		"  api:\n"+
		"    image: %s # keep this comment\n"+
		"    restart: unless-stopped\n"+
		"  worker:\n"+
		"    image: \"%s\"\n",
		e.ref("app", "1.2.0"), e.ref("app", "1.2.0")))
}

// Advancing a pinned tag must carry the candidate's own digest. Pairing the
// new tag with the declared reference's digest is syntactically valid and
// factually wrong.
func TestBumpAdvancesTagAndDigestTogether(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	next := e.push("app", "1.2.0", image{})
	path := e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared))

	got := e.run("bump", "demo")
	assertCode(t, "bump", got.code, 0, got)
	assertEq(t, "compose file", e.read(path), fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.2.0"), next))
}

// Digest pinning is opt-in, and the flag always beats the config.
func TestBumpDigestPinningFollowsConfigThenFlag(t *testing.T) {
	tests := []struct {
		config     string
		args       []string
		wantDigest bool
	}{
		{config: "", wantDigest: false},
		{config: "[bump]\ndigest = true\n", wantDigest: true},
		{config: "[bump]\ndigest = true\n", args: []string{"--digest=false"}, wantDigest: false},
		{config: "[bump]\ndigest = false\n", args: []string{"--digest"}, wantDigest: true},
	}
	for _, tt := range tests {
		e := newEnv(t)
		e.push("app", "1.0.0", image{})
		next := e.push("app", "1.1.0", image{})
		e.config(tt.config)
		path := e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0")))

		got := e.run(append([]string{"bump", "demo"}, tt.args...)...)
		assertCode(t, "bump", got.code, 0, got)
		want := e.ref("app", "1.1.0")
		if tt.wantDigest {
			want += "@" + next
		}
		assertEq(t, fmt.Sprintf("compose file with %q %v", tt.config, tt.args), e.read(path),
			fmt.Sprintf("services:\n  api:\n    image: %s\n", want))
	}
}

// With no newer candidate, digest pinning can still adopt an immutable pin for
// the version already declared -- but only for a version stackmon recognises.
func TestBumpDigestPinsTheCurrentVersionOrRefusesTo(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{})
	opaque := e.push("app", "latest", image{})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n  sonarr:\n    image: %s\n",
		e.ref("app", "1.0.0"), e.ref("app", "latest")))

	got := e.run("bump", "demo", "--digest")
	assertCode(t, "bump", got.code, 0, got)
	assertContains(t, "stdout", got.stdout, "bumped demo/api:")
	assertContains(t, "stderr", got.stderr, "skipped:", "floating tag", "latest")
	assertContains(t, "compose file", e.read(filepath.Join(e.stacks(), "demo", "compose.yaml")),
		e.ref("app", "1.0.0")+"@"+declared)
	assertOmits(t, "compose file", e.read(filepath.Join(e.stacks(), "demo", "compose.yaml")), opaque)
}

// A floating tag has nothing to advance to, and an interpolated reference has
// its pin in .env by the author's choice. Both are refusals, and neither may
// stop the services around them from being bumped.
func TestBumpRefusesFloatingAndInterpolatedReferences(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	e.write(filepath.Join(e.stacks(), "demo", ".env"), "APP_VERSION=1.0.0\n", 0o644)
	body := fmt.Sprintf("services:\n"+
		"  sonarr:\n    image: %s\n"+
		"  web:\n    image: %s:${APP_VERSION}\n"+
		"  api:\n    image: %s\n",
		e.ref("app", "latest"), e.host+"/app", e.ref("app", "1.0.0"))
	e.enrollStack("demo", body)

	got := e.run("bump", "demo")
	assertCode(t, "bump", got.code, 0, got)
	assertContains(t, "stderr", got.stderr, "skipped:", "floating tag", "APP_VERSION", ".env")
	assertContains(t, "stdout", got.stdout, "bumped demo/api:")
	assertOmits(t, "stderr", got.stderr, "error:")

	// The refused services are untouched; the eligible one moved.
	file := e.read(filepath.Join(e.stacks(), "demo", "compose.yaml"))
	assertContains(t, "compose file", file, e.ref("app", "1.1.0"))
	assertContains(t, "compose file", file, "${APP_VERSION}")
}

// Advancing a pin whose candidate digest cannot be resolved would pair a tag
// with the wrong bytes. The service is skipped, loudly, and the run succeeds.
func TestBumpRefusesWhenTheCandidatesDigestCannotBeResolved(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{})
	e.push("app", "9.9.9", image{})
	e.push("tool", "1.0.0", image{})
	e.push("tool", "1.1.0", image{})
	// The tag is listed, but its manifest cannot be fetched: a partial
	// registry outage.
	e.hits.block("/v2/app/manifests/9.9.9")
	path := e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n  ci:\n    image: %s\n",
		e.ref("app", "1.0.0"), declared, e.ref("tool", "1.0.0")))

	got := e.run("bump", "demo")
	assertCode(t, "bump", got.code, 0, got)
	assertContains(t, "stderr", got.stderr, "skipped:", "9.9.9", "digest could not be resolved")
	assertContains(t, "stdout", got.stdout, "bumped demo/ci:")
	assertEq(t, "compose file", e.read(path), fmt.Sprintf("services:\n  api:\n    image: %s@%s\n  ci:\n    image: %s\n",
		e.ref("app", "1.0.0"), declared, e.ref("tool", "1.1.0")))
}

func TestBumpSaysSoWhenThereIsNothingToBump(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{})
	path := e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared))

	got := e.run("bump", "demo")
	assertCode(t, "bump", got.code, 0, got)
	assertContains(t, "stdout", got.stdout, "nothing to bump")
	assertEq(t, "compose file", e.read(path), fmt.Sprintf("services:\n  api:\n    image: %s@%s\n",
		e.ref("app", "1.0.0"), declared))
}

// A dry-run that lies about the pending change is worse than no dry-run.
func TestBumpDryRunPreviewsWithoutWriting(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	before := fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0"))
	path := e.enrollStack("demo", before)

	got := e.run("bump", "demo", "--dry-run")
	assertCode(t, "bump --dry-run", got.code, 0, got)
	assertContains(t, "diff", e.normalize(got.stdout),
		"--- "+e.normalize(path),
		"-    image: "+e.normalize(e.ref("app", "1.0.0")),
		"+    image: "+e.normalize(e.ref("app", "1.1.0")))
	assertEq(t, "compose file is untouched", e.read(path), before)
}

// The guard that makes bump safe to run alongside anything else editing the
// same file. stackmon is frozen mid-run -- it has read the compose file and
// not yet written it -- the file is changed underneath it, and the write is
// refused rather than clobbering the edit. The edit is the same length as the
// original, so only a content comparison catches it.
func TestBumpRefusesAFileChangedWhileItRuns(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	path := e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared))

	gate := e.hits.hold("/v2/app/manifests/")
	run := e.start("bump", "demo")
	gate.wait(t) // the plan is made; the write has not happened

	edited := strings.ReplaceAll(e.read(path), declared, "sha256:"+strings.Repeat("e", 64))
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	gate.open()

	got := run.wait(t)
	assertCode(t, "bump", got.code, 1, got)
	assertContains(t, "stderr", got.stderr, "changed since it was checked", "re-run check")
	assertEq(t, "compose file is untouched", e.read(path), edited)

	// A refused run leaves nothing behind in the user's stack directory.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "compose.yaml" {
		t.Errorf("stack directory holds %v, want only compose.yaml", names(entries))
	}
}

// A dry-run that lies about the pending change is worse than no dry-run, so
// it refuses a file that moved under it exactly as the real write does.
func TestBumpDryRunRefusesAFileChangedWhileItRuns(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	path := e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared))

	gate := e.hits.hold("/v2/app/manifests/")
	run := e.start("bump", "demo", "--dry-run")
	gate.wait(t)

	edited := strings.ReplaceAll(e.read(path), declared, "sha256:"+strings.Repeat("f", 64))
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	gate.open()

	preview := run.wait(t)
	assertCode(t, "bump --dry-run", preview.code, 1, preview)
	assertContains(t, "stderr", preview.stderr, "changed since it was checked")
	assertEq(t, "compose file is untouched", e.read(path), edited)
}

// A stack whose compose file cannot be parsed is a warning in check and must
// read the same way here, or a script sees a different exit code depending on
// which subcommand it ran.
func TestBumpReportsAnUnparseableStackWithoutFailing(t *testing.T) {
	e := newEnv(t)
	e.enrollStack("broken", "services:\n  api:\n    image: ${REQUIRED_IMAGE:?must be set}\n")

	got := e.run("bump", "broken", "--dry-run")
	assertCode(t, "bump on an unparseable stack", got.code, 0, got)
	assertContains(t, "stderr", got.stderr, "warning:", "broken")
}

func TestBumpOnlyRewritesTheNamedService(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	path := e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n  worker:\n    image: %s\n",
		e.ref("app", "1.0.0"), e.ref("app", "1.0.0")))

	got := e.run("bump", "demo", "api")
	assertCode(t, "bump demo api", got.code, 0, got)
	assertEq(t, "compose file", e.read(path), fmt.Sprintf("services:\n  api:\n    image: %s\n  worker:\n    image: %s\n",
		e.ref("app", "1.1.0"), e.ref("app", "1.0.0")))
}

// The file's permissions are the user's, not stackmon's.
func TestBumpPreservesFileMode(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	body := fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0"))
	path := e.write(filepath.Join(e.stacks(), "demo", "compose.yaml"), body, 0o600)
	e.enroll(filepath.Dir(path))

	got := e.run("bump", "demo")
	assertCode(t, "bump", got.code, 0, got)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, "mode", info.Mode().Perm().String(), "-rw-------")
}

// A digest-only pin stays digest-only: pinning it again would change nothing,
// so the run says so rather than pretending it did work.
func TestBumpNotesThatADigestOnlyPinStaysDigestOnly(t *testing.T) {
	e := newEnv(t)
	declared := e.push("proxy", "v3.7.10", image{Version: "v3.7.10"})
	e.push("proxy", "v3.8.0", image{Version: "v3.8.0"})
	next := e.push("proxy", "v3.9.0", image{Version: "v3.9.0"})
	path := e.enrollStack("edge", fmt.Sprintf("services:\n  proxy:\n    image: %s\n", e.pin("proxy", declared)))

	got := e.run("bump", "edge", "--digest")
	assertCode(t, "bump", got.code, 0, got)
	assertContains(t, "stderr", got.stderr, "notice:", "edge/proxy", "already digest-only")
	assertEq(t, "compose file", e.read(path), fmt.Sprintf("services:\n  proxy:\n    image: %s\n", e.pin("proxy", next)))
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name())
	}
	return out
}
