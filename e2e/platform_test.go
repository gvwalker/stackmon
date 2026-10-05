package e2e

import (
	"fmt"
	"testing"
)

// A Service declares the platform it runs on. The metadata stackmon reports
// has to come from that child of the index, not from whichever one stackmon
// happens to default to -- reading the amd64 child's labels for an arm64
// service is a wrong answer with no error to show for it.
func TestCheckReadsThePlatformAServiceDeclares(t *testing.T) {
	e := newEnv(t)
	index := e.pushIndex("multi", "v1",
		archImage{Platform: "linux/amd64", Version: "1.0.0", Revision: "amd-rev"},
		archImage{Platform: "linux/arm64", Version: "1.0.0", Revision: "arm-rev"},
	)
	e.enrollStack("declared", fmt.Sprintf("services:\n  api:\n    image: %s\n    platform: linux/arm64\n", e.ref("multi", "v1")))
	e.enrollStack("implicit", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("multi", "v1")))

	rep := e.checkJSON()
	declared := rep.image(t, "declared", "api")
	implicit := rep.image(t, "implicit", "api")

	assertEq(t, "declared platform", declared.Platform, "linux/arm64")
	assertEq(t, "implicit platform", implicit.Platform, "linux/amd64")
	assertEq(t, "declared revision", declared.Revision, "arm-rev")
	assertEq(t, "implicit revision", implicit.Revision, "amd-rev")

	// The digest is the index's either way. Which child's config blob was
	// read says nothing about which digest the reference names, and a pin on
	// it has to stay portable across every platform the index publishes.
	assertEq(t, "declared digest", declared.RegistryDigest, index)
	assertEq(t, "implicit digest", implicit.RegistryDigest, index)

	// Which child's labels came back is the whole claim, and it is invisible
	// in a status-only transcript: record it so the artifact shows it.
	e.note("platform linux/arm64 declared -> revision %s; no platform declared -> revision %s",
		declared.Revision, implicit.Revision)
}

// An image published for one platform only is a normal image, not a failure:
// a Service declaring that platform has to read it, and stop calling the row
// unknown.
func TestCheckReadsAnArmOnlyImageForAnArmService(t *testing.T) {
	e := newEnv(t)
	e.pushIndex("armonly", "v1", archImage{Platform: "linux/arm64", Version: "1.0.0", Revision: "arm-rev"})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n    platform: linux/arm64\n", e.ref("armonly", "v1")))

	row := e.checkJSON().image(t, "demo", "api")
	assertEq(t, "status", row.Status, "not-running")
	assertEq(t, "revision", row.Revision, "arm-rev")
	e.note("arm-only image read for an arm service: revision %s", row.Revision)
}

// A platform the index does not publish is a fact about the image, not about
// the stack. The row has to say so and name both sides: quietly reporting the
// metadata of a platform nobody asked for is the bug this replaces.
func TestCheckReportsAnUnavailableTargetPlatform(t *testing.T) {
	e := newEnv(t)
	e.pushIndex("amdonly", "1.0.0", archImage{Platform: "linux/amd64", Version: "1.0.0", Revision: "amd-rev"})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n    platform: linux/arm64\n", e.ref("amdonly", "1.0.0")))

	row := e.checkJSON().image(t, "demo", "api")
	assertEq(t, "status", row.Status, "unknown")
	assertEq(t, "declared platform", row.Platform, "linux/arm64")
	assertContains(t, "error", row.Err, "linux/arm64", "linux/amd64")
}

// A single-platform image resolves under any platform: there is no index to
// choose a child from, so declaring one changes nothing.
func TestCheckInspectsASinglePlatformImageWhateverIsDeclared(t *testing.T) {
	e := newEnv(t)
	e.push("solo", "1.0.0", image{Version: "1.0.0", Revision: "solo-rev"})
	e.enrollStack("declared", fmt.Sprintf("services:\n  api:\n    image: %s\n    platform: linux/arm64\n", e.ref("solo", "1.0.0")))

	row := e.checkJSON().image(t, "declared", "api")
	assertEq(t, "status", row.Status, "not-running")
	assertEq(t, "revision", row.Revision, "solo-rev")
}

// The same reference at two platforms is two probes. Their metadata comes
// from different children, so one probe's answer is not the other's -- and
// sharing it is invisible, because both rows would look answered.
func TestCheckProbesOneReferenceOncePerPlatform(t *testing.T) {
	e := newEnv(t)
	e.pushIndex("multi", "v1",
		archImage{Platform: "linux/amd64", Version: "1.0.0", Revision: "amd-rev"},
		archImage{Platform: "linux/arm64", Version: "1.0.0", Revision: "arm-rev"},
	)
	body := func(platform string) string {
		return fmt.Sprintf("services:\n  api:\n    image: %s\n    platform: %s\n", e.ref("multi", "v1"), platform)
	}
	e.enrollStack("amd", body("linux/amd64"))
	e.enrollStack("arm", body("linux/arm64"))

	e.hits.reset()
	rep := e.checkJSON()
	assertEq(t, "amd revision", rep.image(t, "amd", "api").Revision, "amd-rev")
	assertEq(t, "arm revision", rep.image(t, "arm", "api").Revision, "arm-rev")
	e.note("index fetches for multi:v1 across both platforms: %d", e.hits.count("GET /v2/multi/manifests/v1"))
	if n := e.hits.count("GET /v2/multi/manifests/v1"); n != 2 {
		t.Errorf("fetched the shared index %d times, want 2: one per platform", n)
	}
}

// Bump advances a pin to the candidate's own digest, so a candidate resolved
// for one platform has to stay an index digest. Pinning a child manifest
// would break the stack on every platform the index publishes but the one
// declared here.
func TestBumpKeepsAMultiArchPinPortable(t *testing.T) {
	e := newEnv(t)
	children := []archImage{
		{Platform: "linux/amd64", Version: "1.0.0", Revision: "amd-rev"},
		{Platform: "linux/arm64", Version: "1.0.0", Revision: "arm-rev"},
	}
	declared := e.pushIndex("multi", "v1", children...)
	next := e.pushIndex("multi", "v1.1.0",
		archImage{Platform: "linux/amd64", Version: "1.1.0", Revision: "amd-rev2"},
		archImage{Platform: "linux/arm64", Version: "1.1.0", Revision: "arm-rev2"},
	)
	path := e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n    platform: linux/arm64\n",
		e.ref("multi", "v1"), declared))

	got := e.run("bump", "demo")
	assertCode(t, "bump", got.code, 0, got)
	assertEq(t, "compose file", e.read(path), fmt.Sprintf("services:\n  api:\n    image: %s@%s\n    platform: linux/arm64\n",
		e.ref("multi", "v1.1.0"), next))
}

// A row whose metadata came from one platform is unverifiable without saying
// which, so every surface that reports a row reports the platform with it.
func TestEverySurfaceReportsTheEffectivePlatform(t *testing.T) {
	e := newEnv(t)
	e.pushIndex("multi", "v1", archImage{Platform: "linux/arm64", Version: "1.0.0"})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n    platform: linux/arm64\n", e.ref("multi", "v1")))

	_, compact := e.checkCompact()
	assertContains(t, "compact", compact, `"platform": "linux/arm64"`)

	// The table has no platform column: it would say "linux/amd64" on every
	// row of every run to say something about the rows that asked otherwise.
	// DETAIL carries it instead, and only when it is worth saying.
	table := e.run("check")
	assertCode(t, "check", table.code, 0, table)
	assertContains(t, "table", table.stdout, "no running container; platform linux/arm64")

	shown := e.run("show", "demo")
	assertCode(t, "show", shown.code, 0, shown)
	assertContains(t, "detail", shown.stdout, "platform:  linux/arm64")
}

// The quiet is the point: a stack that declares nothing must read exactly as
// it did before platform support existed, with no trace of the default.
func TestTheTableStaysQuietAboutTheDefaultPlatform(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0")))

	got := e.run("check")
	assertCode(t, "check", got.code, 0, got)
	assertContains(t, "table", got.stdout, "STACK", "api", "1.0.0", "update-available", "1.1.0 available (minor)")
	assertOmits(t, "table", got.stdout, "platform", "linux/amd64")
}

// A platform stackmon cannot read is said so, never quietly replaced with the
// default: an unparseable declaration is a typo to fix, and reading amd64
// metadata while the file says something else is the failure this whole change
// exists to remove.
func TestCheckReportsAPlatformItCannotParse(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{Version: "1.0.0"})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n    platform: linux/arm64/v8/extra\n", e.ref("app", "1.0.0")))

	row := e.checkJSON().image(t, "demo", "api")
	assertEq(t, "status", row.Status, "unknown")
	assertEq(t, "platform", row.Platform, "")
	assertContains(t, "error", row.Err, "linux/arm64/v8/extra")

	table := e.run("check")
	assertCode(t, "check", table.code, 0, table)
	assertContains(t, "table", table.stdout, "STACK", "unknown", `parsing platform "linux/arm64/v8/extra"`)
}
