package e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// A .env value with a trailing comment, and a .env value that references
// another .env value, are ordinary Compose input. Read as text they are an
// image reference with a comment inside it, and a reference that never
// resolves -- so both must come back as the tag the author meant.
func TestCheckResolvesDotenvCommentsAndReferences(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	dotenv := strings.Join([]string{
		"# what the compose file interpolates",
		"STACKMON_PINNED=1.0.0 # stable",
		"STACKMON_BASE=1.0.0",
		"STACKMON_DERIVED=${STACKMON_BASE}",
	}, "\n") + "\n"
	e.write(filepath.Join(e.stacks(), "demo", ".env"), dotenv, 0o644)
	e.enrollStack("demo", fmt.Sprintf("services:\n"+
		"  commented: {image: \"%s/app:${STACKMON_PINNED}\"}\n"+
		"  derived: {image: \"%s/app:${STACKMON_DERIVED}\"}\n",
		e.host, e.host))

	got := e.run("check")
	assertCode(t, "check", got.code, 0, got)
	assertOmits(t, "stderr", got.stderr, "warning:")

	rep := e.checkJSON()
	for svc, raw := range map[string]string{
		"commented": e.host + "/app:${STACKMON_PINNED}",
		"derived":   e.host + "/app:${STACKMON_DERIVED}",
	} {
		img := rep.image(t, "demo", svc)
		assertEq(t, svc+" raw", img.Ref.Raw, raw)
		assertEq(t, svc+" resolved", img.Ref.Resolved, e.ref("app", "1.0.0"))
		assertEq(t, svc+" status", img.Status, "update-available")
	}

	// Reading a .env must not rewrite it: bump is the only thing that edits
	// these files, and it never edits this one.
	assertEq(t, ".env untouched", e.read(filepath.Join(e.stacks(), "demo", ".env")), dotenv)
}

// The process environment wins over the file, including when it is empty,
// and it is what a ${VAR} inside the file expands to -- not just an overlay
// applied afterwards. An exported-but-empty value is an override, so
// ${VAR:-default} takes the default rather than the file's value.
func TestCheckDotenvFollowsProcessEnvironmentPrecedence(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	e.push("app", "9.9.9", image{})
	e.write(filepath.Join(e.stacks(), "demo", ".env"),
		"STACKMON_PINNED=1.0.0\n"+
			"STACKMON_BASE=1.0.0\n"+
			"STACKMON_DERIVED=${STACKMON_BASE}\n"+
			"STACKMON_BLANK=1.0.0\n", 0o644)
	e.set("STACKMON_PINNED", "1.1.0")
	e.set("STACKMON_BASE", "1.1.0")
	e.set("STACKMON_BLANK", "")
	e.enrollStack("demo", fmt.Sprintf("services:\n"+
		"  overridden: {image: \"%s/app:${STACKMON_PINNED}\"}\n"+
		"  derived: {image: \"%s/app:${STACKMON_DERIVED}\"}\n"+
		"  blank: {image: \"%s/app:${STACKMON_BLANK:-9.9.9}\"}\n",
		e.host, e.host, e.host))

	got := e.run("check")
	assertCode(t, "check", got.code, 0, got)
	assertOmits(t, "stderr", got.stderr, "warning:")

	rep := e.checkJSON()
	assertEq(t, "process value wins", rep.image(t, "demo", "overridden").Ref.Resolved, e.ref("app", "1.1.0"))
	assertEq(t, "process value expands file reference", rep.image(t, "demo", "derived").Ref.Resolved, e.ref("app", "1.1.0"))
	// 1.0.0 here would mean the file's value survived the empty override.
	assertEq(t, "empty process value overrides", rep.image(t, "demo", "blank").Ref.Resolved, e.ref("app", "9.9.9"))
}

// Most stacks have no .env at all, and a reference with a default in the
// compose file is how they carry the tag. A missing file is not an error.
func TestCheckWithoutDotenvKeepsComposeDefaults(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.enrollStack("demo", fmt.Sprintf("services:\n  web: {image: \"%s/app:${STACKMON_ABSENT:-1.0.0}\"}\n", e.host))

	got := e.run("check")
	assertCode(t, "check", got.code, 0, got)
	assertOmits(t, "stderr", got.stderr, "warning:")

	rep := e.checkJSON()
	assertEq(t, "resolved", rep.image(t, "demo", "web").Ref.Resolved, e.ref("app", "1.0.0"))
}

// A .env stackmon cannot use is reported against that file, by path and by
// what went wrong, with the malformed line's value left out: an unterminated
// quote in a credentials file must not reach the terminal. Neither failure is
// allowed to hide the stacks that did load.
func TestCheckRejectsUnusableDotenvAndKeepsOtherStacks(t *testing.T) {
	const secret = "s3cret-token-value"

	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})

	body := fmt.Sprintf("services:\n  web: {image: \"%s/app:1.0.0\"}\n", e.host)
	e.enrollStack("good", body)

	// An unterminated quote: the parser's own message quotes the whole
	// line, secret included.
	e.write(filepath.Join(e.stacks(), "broken", ".env"), "STACKMON_TOKEN=\""+secret+"\n", 0o644)
	e.enrollStack("broken", body)

	// A directory where the file belongs, which is a read failure rather
	// than a syntax error.
	e.mkdir(filepath.Join(e.stacks(), "directory", ".env"))
	e.enrollStack("directory", body)

	got := e.run("check")
	assertCode(t, "check", got.code, 0, got)
	assertContains(t, "stderr", got.stderr,
		"warning:", "broken", ".env", "invalid .env syntax",
		"directory", "is a directory")
	assertOmits(t, "stderr", got.stderr, secret)

	rep := e.checkJSON()
	assertEq(t, "healthy stack reported", rep.image(t, "good", "web").Status, "update-available")
	for _, img := range rep.Images {
		if img.Stack != "good" {
			t.Errorf("stack %s reported despite an unusable .env", img.Stack)
		}
	}
}
