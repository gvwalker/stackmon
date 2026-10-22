package e2e

// Deadline scenarios: a server that accepts a request but never finishes it
// must end as a bounded failure with the reason in the output, never as a
// run that hangs. Every scenario here sets STACKMON_E2E_SHORT_DEADLINES,
// which the e2e build of the binary answers by shrinking the shipped request
// budgets to two seconds, so proving a deadline fires costs seconds instead
// of the real budget. Each run is started in the background and waited on,
// so a binary that forgot its deadline fails the fence rather than parking
// the suite.

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// runBounded runs the binary in the background and waits for it, so a
// scenario whose subject forgot its deadline fails at the wait fence
// instead of hanging the scenario: proving a run is bounded needs a bound on
// the wait for it.
func (e *env) runBounded(bin string, args ...string) result {
	e.t.Helper()
	return e.startAs(nil, bin, args...).wait(e.t)
}

// assertBounded fails the scenario when a run that had to end at a deadline
// took wall-clock time no deadline could explain.
func assertBounded(t *testing.T, what string, elapsed, limit time.Duration) {
	t.Helper()
	if elapsed > limit {
		t.Errorf("%s took %s, want under %s: no deadline bounded it", what, elapsed, limit)
	}
}

// A registry probe that never gets an answer -- headers that never arrive,
// or a manifest body that never finishes -- must degrade exactly its own
// rows to unknown with the deadline in the reason, while the healthy rows
// keep their verdicts.
func TestCheckBoundsAStalledRegistryProbe(t *testing.T) {
	e := newEnv(t)
	e.set("STACKMON_E2E_SHORT_DEADLINES", "1")
	e.push("fine", "1.0.0", image{})
	e.push("hung", "1.0.0", image{})
	e.push("trunc", "1.0.0", image{})
	e.enrollStack("fine", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("fine", "1.0.0")))
	e.enrollStack("hung", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("hung", "1.0.0")))
	e.enrollStack("trunc", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("trunc", "1.0.0")))
	e.hits.reset()
	e.hits.stall("/v2/hung/manifests", stallHeaders)
	e.hits.stall("/v2/trunc/manifests", stallBody)

	start := time.Now()
	r := e.runBounded(devBinary, "check", "--compact")
	assertBounded(t, "check against a stalled registry", time.Since(start), 30*time.Second)
	assertCode(t, "check", r.code, 0, r)
	var rep compactReport
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
		t.Fatalf("check --compact: %v\n%s", err, r.stdout)
	}

	hung := rep.image(t, "hung", "api")
	assertEq(t, "stalled-header row status", hung.Status, "unknown")
	assertContains(t, "stalled-header row error", hung.Err, "deadline exceeded")
	trunc := rep.image(t, "trunc", "api")
	assertEq(t, "stalled-body row status", trunc.Status, "unknown")
	assertContains(t, "stalled-body row error", trunc.Err, "deadline exceeded")
	fine := rep.image(t, "fine", "api")
	// The daemon answers with no container, so the honest verdict for the
	// healthy row is not-running: the point is that it keeps a real verdict
	// while the stalled rows degrade to unknown.
	assertEq(t, "healthy row status", fine.Status, "not-running")
	assertEq(t, "healthy row error", fine.Err, "")

	e.note("registry requests: healthy manifests=%d tags=%d, stalled-header manifests=%d, stalled-body manifests=%d",
		e.hits.count("GET /v2/fine/manifests"), e.hits.count("GET /v2/fine/tags"),
		e.hits.count("GET /v2/hung/manifests"), e.hits.count("GET /v2/trunc/manifests"))
}

// Release notes are best-effort: a GitHub that stalls the release listing
// must cost the detail view its notes and nothing else -- the verdicts stay,
// the run finishes, and nothing hangs.
func TestShowBoundsStalledReleaseNotes(t *testing.T) {
	e := newEnv(t)
	e.set("STACKMON_E2E_SHORT_DEADLINES", "1")
	hungSrc := "https://github.com/e2e/hung"
	truncSrc := "https://github.com/e2e/trunc"
	e.push("hung", "1.0.0", image{Version: "1.0.0", Source: hungSrc})
	e.push("hung", "1.1.0", image{Version: "1.1.0", Source: hungSrc})
	e.push("trunc", "1.0.0", image{Version: "1.0.0", Source: truncSrc})
	e.push("trunc", "1.1.0", image{Version: "1.1.0", Source: truncSrc})
	e.gh.stall("/repos/e2e/hung/releases", stallHeaders)
	e.gh.stall("/repos/e2e/trunc/releases", stallBody)
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n  web:\n    image: %s\n",
		e.ref("hung", "1.0.0"), e.ref("trunc", "1.0.0")))

	start := time.Now()
	r := e.runBounded(devBinary, "show", "demo")
	assertBounded(t, "show with stalled release notes", time.Since(start), 30*time.Second)
	assertCode(t, "show", r.code, 0, r)
	assertContains(t, "detail", r.stdout,
		"demo / api", "demo / web", "status:    update-available", "available: 1.1.0",
		"release notes", "no release notes available")

	e.note("GitHub requests: stalled-header releases=%d, stalled-body releases=%d",
		e.gh.count("/repos/e2e/hung/releases"), e.gh.count("/repos/e2e/trunc/releases"))
}

// The report's own reproduction: a GitHub API that stalls an answer strands
// `whats-new`. The request budget turns it into a bounded failure that says
// what happened, which is the update check's own answer to give.
func TestWhatsNewBoundsAStalledGitHubAPI(t *testing.T) {
	e := newEnv(t)
	e.set("STACKMON_E2E_SHORT_DEADLINES", "1")
	e.gh.publish("gvwalker/stackmon", ghRelease{Tag: "v9.9.9", Body: "A release."})
	e.gh.stall("/repos/gvwalker/stackmon/releases", stallHeaders)

	start := time.Now()
	r := e.runBounded(taggedBinary, "whats-new")
	assertBounded(t, "whats-new against a stalled GitHub API", time.Since(start), 30*time.Second)
	assertCode(t, "whats-new", r.code, 1, r)
	assertContains(t, "stderr", r.stderr, "error:", "selfupdate: fetching latest release", "deadline exceeded")

	e.note("GitHub requests: releases=%d", e.gh.count("/repos/gvwalker/stackmon/releases"))
}

// A stalled asset download must fail within its budget and leave the
// installed executable exactly as it was: a hung transfer is a failed
// update, not a half of one.
func TestUpdateBoundsAStalledDownload(t *testing.T) {
	e := newEnv(t)
	e.set("STACKMON_E2E_SHORT_DEADLINES", "1")
	payload := []byte("#!/bin/sh\necho the new release\n")
	asset := "/dl/" + assetName()
	e.gh.stall(asset, stallBody)
	e.gh.publish("gvwalker/stackmon", ghRelease{
		Tag: "v9.9.9",
		Assets: []ghAsset{{
			Name:   assetName(),
			URL:    e.gh.server.URL + asset,
			Digest: "sha256:" + sum(payload),
		}},
	})
	bin := e.installCopy()
	before := e.read(bin)

	start := time.Now()
	r := e.runBounded(bin, "update")
	assertBounded(t, "update with a stalled download", time.Since(start), 30*time.Second)
	assertCode(t, "update", r.code, 1, r)
	assertContains(t, "stderr", r.stderr, "error:", "selfupdate: downloading", "deadline exceeded")
	assertEq(t, "installed binary", e.read(bin), before)
	assertNoUpdateTemps(t, bin)

	e.note("GitHub requests: releases=%d asset=%d; installed binary unchanged, no update temporaries remain",
		e.gh.count("/repos/gvwalker/stackmon/releases"), e.gh.count(asset))
}
