package e2e

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
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
	assertNoUpdateTemps(t, bin)
	e.note("checksum mismatch refused; installed binary unchanged; no update temporary files remain")
}

// Exercise the shipped limit through the CLI, without allocating an oversized
// payload in the fixture. An exact-limit asset must succeed; one more byte must
// fail, including when the server omits Content-Length (chunked HTTP/1.1).
func TestUpdateBoundsAssetSize(t *testing.T) {
	const limit = 128 << 20
	for _, tc := range []struct {
		name    string
		size    int64
		chunked bool
	}{
		{"exact limit", limit, false},
		{"oversized declared length", limit + 1, false},
		{"oversized chunked", limit + 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			h := sha256.New()
			if _, err := io.CopyN(h, zeroBytes{}, tc.size); err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.chunked {
					w.(http.Flusher).Flush()
				} else {
					w.Header().Set("Content-Length", fmt.Sprint(tc.size))
				}
				_, _ = io.CopyN(w, zeroBytes{}, tc.size)
			}))
			t.Cleanup(srv.Close)
			e.publishDownload(srv.URL, fmt.Sprintf("sha256:%x", h.Sum(nil)))
			bin := e.installCopy()
			before := sum([]byte(e.read(bin)))
			got := e.runBinary(bin, "update")
			if tc.size > limit {
				assertCode(t, "update", got.code, 1, got)
				assertContains(t, "stderr", got.stderr, "exceeds maximum asset size of 134217728 bytes")
				assertEq(t, "original executable digest", sum([]byte(e.read(bin))), before)
				e.note("128 MiB + 1 byte refused; original executable SHA-256 unchanged")
			} else {
				assertCode(t, "update", got.code, 0, got)
				f, err := os.Open(bin)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				installed := sha256.New()
				n, err := io.Copy(installed, f)
				if err != nil {
					t.Fatal(err)
				}
				assertEq(t, "installed bytes", n, tc.size)
				assertEq(t, "installed SHA-256", fmt.Sprintf("%x", installed.Sum(nil)), fmt.Sprintf("%x", h.Sum(nil)))
				e.note("exact 128 MiB asset installed; size and SHA-256 verified")
			}
			assertNoUpdateTemps(t, bin)
			e.note("GitHub release requests=%d; no update temporary files remain", e.gh.count("/repos/gvwalker/stackmon/releases"))
		})
	}
}

type zeroBytes struct{}

func (zeroBytes) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func (e *env) publishDownload(url, digest string) {
	e.t.Helper()
	e.gh.publish("gvwalker/stackmon", ghRelease{
		Tag:    "v9.9.9",
		Assets: []ghAsset{{Name: assetName(), URL: url, Digest: digest}},
	})
}

// A paused chunked response proves bytes reach disk before EOF, and the old
// executable remains usable until verification completes. Finish with either
// the valid digest or a mismatch to exercise cleanup of an already-written temp.
func TestUpdateStreamsBeforeVerification(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(fmt.Sprintf("valid=%t", valid), func(t *testing.T) {
			e := newEnv(t)
			payload := []byte("#!/bin/sh\necho streamed release\n#" + strings.Repeat("x", 256<<10) + "\n")
			sent, resume := make(chan struct{}), make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(payload)
				w.(http.Flusher).Flush()
				close(sent)
				<-resume
			}))
			t.Cleanup(srv.Close)
			// Release the handler before server cleanup, even if an assertion fails.
			t.Cleanup(func() { close(resume) })
			digest := "sha256:" + sum(payload)
			if !valid {
				digest = "sha256:" + strings.Repeat("0", 64)
			}
			e.publishDownload(srv.URL, digest)
			bin := e.installCopy()
			before := e.read(bin)
			pending := e.startAs(nil, bin, "update")
			select {
			case <-sent:
			case <-time.After(10 * time.Second):
				t.Fatal("download did not reach the body")
			}
			streamed := false
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				files, err := filepath.Glob(filepath.Join(filepath.Dir(bin), ".stackmon-update-*"))
				if err != nil {
					t.Fatal(err)
				}
				if len(files) == 1 {
					info, err := os.Stat(files[0])
					if err == nil && info.Size() == int64(len(payload)) {
						streamed = true
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			assertEq(t, "payload on disk before response EOF", streamed, true)
			assertEq(t, "executable before verification", e.read(bin), before)
			e.note("chunked bytes staged before response EOF=%t; original executable unchanged before verification", streamed)
			resume <- struct{}{}
			got := pending.wait(t)
			if valid {
				assertCode(t, "update", got.code, 0, got)
				assertEq(t, "installed binary", e.read(bin), string(payload))
				installed := e.runBinary(bin, "--version")
				assertContains(t, "installed binary", installed.stdout, "streamed release")
			} else {
				assertCode(t, "update", got.code, 1, got)
				assertContains(t, "stderr", got.stderr, "checksum mismatch")
				assertEq(t, "original executable", e.read(bin), before)
			}
			assertNoUpdateTemps(t, bin)
			e.note("verified replacement installed=%t; no update temporary files remain", valid)
		})
	}
}

func TestUpdateRefusesATruncatedDownload(t *testing.T) {
	e := newEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		_, _ = w.Write([]byte("incomplete"))
	}))
	t.Cleanup(srv.Close)
	e.publishDownload(srv.URL, "sha256:"+sum([]byte("incomplete")))
	bin := e.installCopy()
	before := e.read(bin)
	got := e.runBinary(bin, "update")
	assertCode(t, "update", got.code, 1, got)
	assertContains(t, "stderr", got.stderr, "unexpected EOF")
	assertEq(t, "original executable", e.read(bin), before)
	assertNoUpdateTemps(t, bin)
	e.note("inaccurate Content-Length refused; original executable unchanged; no update temporary files remain")
}

func TestUpdateRefusesAnUnsupportedDigest(t *testing.T) {
	e := newEnv(t)
	e.publishStackmonRelease("v9.9.9", []byte("unverified"), "sha512:"+strings.Repeat("0", 128))
	bin := e.installCopy()
	before := e.read(bin)
	got := e.runBinary(bin, "update")
	assertCode(t, "update", got.code, 1, got)
	assertContains(t, "stderr", got.stderr, "no sha256 digest")
	assertEq(t, "original executable", e.read(bin), before)
	assertEq(t, "asset requests", e.gh.count("/dl/"), 0)
	assertNoUpdateTemps(t, bin)
	e.note("unsupported digest refused without fetching asset; original executable unchanged; no update temporary files remain")
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

func TestUpdatePreservesInstalledMetadata(t *testing.T) {
	e := newEnv(t)
	payload := []byte("#!/bin/sh\necho replacement\n")
	e.publishStackmonRelease("v9.9.9", payload, "")
	bin := e.installCopy()
	if err := os.Chmod(bin, 0o751); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(bin)
	if err != nil {
		t.Fatal(err)
	}
	got := e.runBinary(bin, "update")
	assertCode(t, "update", got.code, 0, got)
	after, err := os.Stat(bin)
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, "installed mode", after.Mode(), before.Mode())
	assertEq(t, "installed binary", e.read(bin), string(payload))
	e.note("replacement content verified; installed mode preserved as 0751")
}

func TestUpdateRefusesANonWritableInstallDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	e := newEnv(t)
	e.publishStackmonRelease("v9.9.9", []byte("replacement"), "")
	bin := e.installCopy()
	before := e.read(bin)
	dir := filepath.Dir(bin)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	got := e.exec(bin, "update")
	assertCode(t, "update", got.code, 1, got)
	assertContains(t, "stderr", got.stderr, "creating temp file", "permission denied")
	got.stderr = regexp.MustCompile(`\.stackmon-update-[0-9]+`).ReplaceAllString(got.stderr, ".stackmon-update-TEMP")
	e.record([]string{"update"}, got)
	assertEq(t, "original binary", e.read(bin), before)
	assertNoUpdateTemps(t, bin)
	e.note("original binary unchanged; no update temporary files remain")
}

func assertNoUpdateTemps(t *testing.T, bin string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(filepath.Dir(bin), ".stackmon-update-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("update temporary files: %v (%v)", files, err)
	}
}
