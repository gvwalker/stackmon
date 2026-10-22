package e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// A compose file may declare one image and let several services share it: a
// YAML merge key pulls a whole mapping into a service, an alias points at a
// value written elsewhere. Compose resolves both into an effective image, so
// stackmon must report the service that inherits it -- dropping it used to
// drop the whole stack, and with it every sibling that was perfectly
// readable.
func TestCheckReportsInheritedImagesAndTheirSiblings(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	body := fmt.Sprintf("x-base: &base\n"+
		"  image: %s\n"+
		"  restart: unless-stopped\n"+
		"services:\n"+
		"  web:\n"+
		"    <<: *base\n"+
		"  api:\n"+
		"    image: %s\n",
		e.ref("app", "1.0.0"), e.ref("app", "1.0.0"))
	e.enrollStack("demo", body)

	got := e.run("check")
	assertCode(t, "check", got.code, 0, got)
	assertOmits(t, "stderr", got.stderr, "warning:", "no image line found")
	assertContains(t, "table", got.stdout, "web", "api", "update-available")

	e.hits.reset()
	rep := e.checkJSON()
	web := rep.image(t, "demo", "web")
	assertEq(t, "web status", web.Status, "update-available")
	assertEq(t, "web candidate", web.Candidate, "1.1.0")
	assertEq(t, "web version", web.Version, "1.0.0")
	assertEq(t, "web origin", web.Origin, "merge")
	api := rep.image(t, "demo", "api")
	assertEq(t, "api status", api.Status, "update-available")
	assertEq(t, "api origin", api.Origin, "own")

	// The inherited service is monitored like any other: the registry is
	// asked about the image it runs, once, however many services run it.
	e.note("manifest fetches for the image both services run: %d", e.hits.count("GET /v2/app/manifests/1.0.0"))
	if n := e.hits.count("GET /v2/app/manifests/1.0.0"); n != 1 {
		t.Errorf("fetched the manifest %d times, want 1", n)
	}

	// The whole stack was checked: nothing was skipped, and no service was
	// dropped on a warning.
	assertEq(t, "stacks checked", rep.Diagnostics.StacksChecked, 1)
	assertEq(t, "stacks skipped", rep.Diagnostics.StacksSkipped, 0)
	assertEq(t, "services checked", rep.Diagnostics.ServicesChecked, 2)
	assertEq(t, "services skipped", rep.Diagnostics.ServicesSkipped, 0)

	// The detail view says where the value came from, so a reader knows
	// why the row cannot be bumped.
	shown := e.run("show", "demo")
	assertCode(t, "show", shown.code, 0, shown)
	assertContains(t, "detail", shown.stdout, "demo / web", "origin:    inherited from a YAML merge key")
}

// The value behind an alias is written once and used by name. The service
// that uses it is reported like any other; the anchor it points at is
// somebody else's text.
func TestCheckReportsAnAliasedImage(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	body := fmt.Sprintf("x-img: &img %s\n"+
		"services:\n"+
		"  web:\n"+
		"    image: *img\n"+
		"  api:\n"+
		"    image: %s\n",
		e.ref("app", "1.0.0"), e.ref("app", "1.0.0"))
	e.enrollStack("demo", body)

	got := e.run("check")
	assertCode(t, "check", got.code, 0, got)
	assertOmits(t, "stderr", got.stderr, "warning:")

	rep := e.checkJSON()
	web := rep.image(t, "demo", "web")
	assertEq(t, "web status", web.Status, "update-available")
	assertEq(t, "web candidate", web.Candidate, "1.1.0")
	assertEq(t, "web origin", web.Origin, "alias")
	assertEq(t, "api origin", rep.image(t, "demo", "api").Origin, "own")
}

// Compose's own extends inherits an image from another service. The
// extending service is reported; the service it extends keeps its own
// literal, but that literal now has a consumer.
func TestCheckReportsAnExtendsInheritedImage(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	body := fmt.Sprintf("services:\n"+
		"  base:\n"+
		"    image: %s\n"+
		"  web:\n"+
		"    extends:\n"+
		"      service: base\n",
		e.ref("app", "1.0.0"))
	e.enrollStack("demo", body)

	got := e.run("check")
	assertCode(t, "check", got.code, 0, got)
	assertOmits(t, "stderr", got.stderr, "warning:")

	rep := e.checkJSON()
	web := rep.image(t, "demo", "web")
	assertEq(t, "web status", web.Status, "update-available")
	assertEq(t, "web candidate", web.Candidate, "1.1.0")
	assertEq(t, "web origin", web.Origin, "extends")
	assertEq(t, "base origin", rep.image(t, "demo", "base").Origin, "shared")
}

// A service Compose pulls in from another file is declared outside the file
// stackmon reads. It is still reported: the stack it belongs to must not
// disappear because one of its services was written elsewhere.
func TestCheckReportsAServiceDeclaredInAnIncludedFile(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	e.write(filepath.Join(e.stacks(), "demo", "other.yaml"),
		fmt.Sprintf("services:\n  extra:\n    image: %s\n", e.ref("app", "1.0.0")), 0o644)
	body := fmt.Sprintf("include:\n  - other.yaml\nservices:\n  api:\n    image: %s\n", e.ref("app", "1.0.0"))
	e.enrollStack("demo", body)

	got := e.run("check")
	assertCode(t, "check", got.code, 0, got)
	assertOmits(t, "stderr", got.stderr, "warning:")

	rep := e.checkJSON()
	extra := rep.image(t, "demo", "extra")
	assertEq(t, "extra status", extra.Status, "update-available")
	assertEq(t, "extra candidate", extra.Candidate, "1.1.0")
	assertEq(t, "extra origin", extra.Origin, "external")
	assertEq(t, "api origin", rep.image(t, "demo", "api").Origin, "own")
}

// A row with nothing to update still says where its image came from: the
// next update will meet the same refusal.
func TestCheckSaysWhereAnInheritedImageCameFromWithoutAnUpdate(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	body := fmt.Sprintf("x-base: &base\n  image: %s\nservices:\n  web:\n    <<: *base\n", e.ref("app", "1.0.0"))
	e.enrollStack("demo", body)

	got := e.run("check")
	assertCode(t, "check", got.code, 0, got)
	assertContains(t, "table", got.stdout, "web", "not-running", "image inherited from a YAML merge key; bump will not rewrite it")
}

// The inherited declaration is somebody else's text: bump refuses to rewrite
// it and says why, and the sibling that does own its literal still moves.
func TestBumpRefusesToRewriteAnInheritedImage(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	body := fmt.Sprintf("x-base: &base\n"+
		"  image: %s\n"+
		"services:\n"+
		"  web:\n"+
		"    <<: *base\n"+
		"  api:\n"+
		"    image: %s\n",
		e.ref("app", "1.0.0"), e.ref("app", "1.0.0"))
	path := e.enrollStack("demo", body)

	got := e.run("bump", "demo")
	assertCode(t, "bump", got.code, 0, got)
	assertContains(t, "stderr", got.stderr, "skipped:", "demo/web", "merge key")
	assertContains(t, "stdout", got.stdout, "bumped demo/api:")
	assertOmits(t, "stdout", got.stdout, "bumped demo/web:")

	// The anchor's text is untouched; only the service that owns its
	// literal moved.
	e.note("compose file after the bump:\n%s", e.normalize(e.read(path)))
	assertEq(t, "compose file", e.read(path), fmt.Sprintf("x-base: &base\n"+
		"  image: %s\n"+
		"services:\n"+
		"  web:\n"+
		"    <<: *base\n"+
		"  api:\n"+
		"    image: %s\n",
		e.ref("app", "1.0.0"), e.ref("app", "1.1.0")))
}

// A preview must show the same rewrite the real run would make: the
// inherited declaration produces no diff at all.
func TestBumpDryRunPreviewsOnlyTheRewritableService(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	body := fmt.Sprintf("x-base: &base\n"+
		"  image: %s\n"+
		"services:\n"+
		"  web:\n"+
		"    <<: *base\n"+
		"  api:\n"+
		"    image: %s\n",
		e.ref("app", "1.0.0"), e.ref("app", "1.0.0"))
	path := e.enrollStack("demo", body)

	got := e.run("bump", "demo", "--dry-run")
	assertCode(t, "bump --dry-run", got.code, 0, got)
	assertContains(t, "stderr", got.stderr, "skipped:", "demo/web")
	diff := e.normalize(got.stdout)
	assertContains(t, "diff", diff, "-    image: "+e.normalize(e.ref("app", "1.0.0")))
	assertContains(t, "diff", diff, "+    image: "+e.normalize(e.ref("app", "1.1.0")))
	if n := strings.Count(diff, "--- "); n != 1 {
		t.Errorf("diff holds %d file headers, want 1:\n%s", n, diff)
	}
	assertEq(t, "compose file is untouched", e.read(path), body)
}

// One anchor, two services: rewriting the literal would change the service
// the user did not select, so neither is rewritten and the file is left
// exactly as it was.
func TestBumpRefusesToRewriteASharedAnchor(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	body := fmt.Sprintf("services:\n"+
		"  web:\n"+
		"    image: &shared %s\n"+
		"  api:\n"+
		"    image: *shared\n",
		e.ref("app", "1.0.0"))
	path := e.enrollStack("demo", body)

	got := e.run("bump", "demo")
	assertCode(t, "bump", got.code, 0, got)
	assertContains(t, "stderr", got.stderr, "skipped:", "demo/web", "shared", "demo/api")
	assertOmits(t, "stdout", got.stdout, "bumped")
	e.note("compose file after the refused bump:\n%s", e.normalize(e.read(path)))
	assertEq(t, "compose file is untouched", e.read(path), body)
}

// A service that extends another one has a consumer of that service's
// literal: rewriting it would change the extending service too.
func TestBumpRefusesToRewriteAnExtendedService(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	body := fmt.Sprintf("services:\n"+
		"  base:\n"+
		"    image: %s\n"+
		"  web:\n"+
		"    extends:\n"+
		"      service: base\n",
		e.ref("app", "1.0.0"))
	path := e.enrollStack("demo", body)

	got := e.run("bump", "demo")
	assertCode(t, "bump", got.code, 0, got)
	assertContains(t, "stderr", got.stderr, "skipped:", "demo/base", "shared", "demo/web")
	assertOmits(t, "stdout", got.stdout, "bumped")
	assertEq(t, "compose file is untouched", e.read(path), body)
}

// An anchor nobody else uses is the service's own literal: rewriting it
// changes that service and nothing else, so it is rewritten exactly.
func TestBumpRewritesAnAnchorOnlyItsOwnServiceUses(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	body := fmt.Sprintf("services:\n  web:\n    image: &own %s\n", e.ref("app", "1.0.0"))
	path := e.enrollStack("demo", body)

	got := e.run("bump", "demo")
	assertCode(t, "bump", got.code, 0, got)
	assertContains(t, "stdout", got.stdout, "bumped demo/web:")
	assertEq(t, "compose file", e.read(path), fmt.Sprintf("services:\n  web:\n    image: &own %s\n", e.ref("app", "1.1.0")))
}
