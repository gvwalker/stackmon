package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// publishStackmonRelease puts a stackmon release on the fake GitHub, with an
// asset for this platform whose reported digest is that payload's real sha256.
// Pass digest to declare a different one, as a substituted or corrupted
// download would.
func (e *env) publishStackmonRelease(tag string, payload []byte, digest string) {
	e.t.Helper()
	if digest == "" {
		digest = "sha256:" + sum(payload)
	}
	e.gh.publish("gvwalker/stackmon", ghRelease{
		Tag:  tag,
		Body: "Release " + tag,
		Assets: []ghAsset{
			{Name: assetName(), URL: e.gh.file("/dl/"+assetName(), payload), Digest: digest},
		},
	})
}

// installCopy puts a copy of the built binary where a user would have
// installed it, and returns its path. `update` replaces the running binary,
// so the real build directory must not be the one it writes to.
func (e *env) installCopy() string {
	e.t.Helper()
	src, err := os.ReadFile(devBinary)
	if err != nil {
		e.t.Fatal(err)
	}
	return e.write(filepath.Join(e.root, "bin", "stackmon"), string(src), 0o755)
}

func TestWhatsNewComparesTheRunningVersionWithTheLatest(t *testing.T) {
	behind := newEnv(t)
	behind.gh.publish("gvwalker/stackmon",
		ghRelease{Tag: "v9.9.9", Body: "A later release."},
		ghRelease{Tag: taggedVersion, Body: "The release you are running."},
	)
	got := behind.runBinary(taggedBinary, "whats-new")
	assertCode(t, "whats-new", got.code, 0, got)
	assertContains(t, "whats-new", got.stdout,
		"stackmon "+taggedVersion, "The release you are running.",
		"update available: "+taggedVersion+" -> v9.9.9", "stackmon update")

	current := newEnv(t)
	current.gh.publish("gvwalker/stackmon", ghRelease{Tag: taggedVersion, Body: "The release you are running."})
	up := current.runBinary(taggedBinary, "whats-new")
	assertCode(t, "whats-new", up.code, 0, up)
	assertContains(t, "whats-new", up.stdout, "you are running the latest version")
	assertOmits(t, "whats-new", up.stdout, "update available")
}

// A build with no comparable version must say so rather than claim to be
// current, which is how a dev build would silently never update.
func TestWhatsNewOnADevBuildSaysItCannotCompare(t *testing.T) {
	e := newEnv(t)
	e.gh.publish("gvwalker/stackmon", ghRelease{Tag: "v9.9.9", Body: "A release."})

	got := e.run("whats-new")
	assertCode(t, "whats-new", got.code, 0, got)
	assertContains(t, "whats-new", got.stdout, "dev build", "latest release is v9.9.9", "stackmon update")
}

func TestUpdateInstallsAVerifiedRelease(t *testing.T) {
	e := newEnv(t)
	payload := []byte("#!/bin/sh\necho installed from the release\n")
	e.publishStackmonRelease("v9.9.9", payload, "")
	bin := e.installCopy()

	got := e.runBinary(bin, "update")
	assertCode(t, "update", got.code, 0, got)
	assertContains(t, "update", got.stdout, "running a dev build", "updating stackmon dev -> v9.9.9...", "updated to v9.9.9")
	assertEq(t, "installed binary", e.read(bin), string(payload))

	// The installed binary is the release, not a stub: run it.
	installed := e.runBinary(bin, "--version")
	assertCode(t, "installed binary", installed.code, 0, installed)
	assertContains(t, "installed binary", installed.stdout, "installed from the release")
}

// An install that cannot be verified must leave the working binary alone: a
// corrupted or substituted asset is worse than no update.
func TestUpdateRefusesAnUnverifiedRelease(t *testing.T) {
	e := newEnv(t)
	payload := []byte("payload that does not match the digest GitHub reports")
	e.publishStackmonRelease("v9.9.9", payload, "sha256:"+strings.Repeat("0", 64))
	bin := e.installCopy()
	before := e.read(bin)

	got := e.runBinary(bin, "update")
	assertCode(t, "update", got.code, 1, got)
	assertContains(t, "stderr", got.stderr, "error:", "checksum mismatch")
	assertEq(t, "binary is untouched", e.read(bin), string(before))
}

// A release with no build for the platform running stackmon is a dead end,
// not a silent no-op.
func TestUpdateReportsAMissingAssetForThisPlatform(t *testing.T) {
	e := newEnv(t)
	e.gh.publish("gvwalker/stackmon", ghRelease{
		Tag:    "v9.9.9",
		Assets: []ghAsset{{Name: "stackmon-some-other-os", URL: e.gh.file("/dl/other", []byte(""))}},
	})
	bin := e.installCopy()

	got := e.runBinary(bin, "update")
	assertCode(t, "update", got.code, 1, got)
	assertContains(t, "stderr", got.stderr, "error:", "no asset for")
}

// A release that does not say what its asset should hash to cannot be
// verified, and an unverifiable download must never be installed: skipping
// the check is the one failure mode the check exists to prevent.
func TestUpdateRefusesAnAssetGitHubReportsNoDigestFor(t *testing.T) {
	e := newEnv(t)
	payload := []byte("a perfectly good binary")
	// An API that predates asset digests reports the asset and no digest.
	e.gh.publish("gvwalker/stackmon", ghRelease{
		Tag:    "v9.9.9",
		Assets: []ghAsset{{Name: assetName(), URL: e.gh.file("/dl/"+assetName(), payload)}},
	})
	bin := e.installCopy()
	before := e.read(bin)

	got := e.runBinary(bin, "update")
	assertCode(t, "update", got.code, 1, got)
	assertContains(t, "stderr", got.stderr, "error:", "no sha256 digest")
	assertEq(t, "binary is untouched", e.read(bin), string(before))
}
