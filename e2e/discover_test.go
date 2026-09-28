package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Discovery has to agree with what `enroll <path>` will accept, and must not
// wander into dot directories or past its depth limit.
func TestDiscoverFindsStacksBelowConfiguredRoots(t *testing.T) {
	e := newEnv(t)
	for _, rel := range []string{
		"adguard/compose.yaml",
		"traefik/docker-compose.yml",
		"both/compose.yaml",
		"both/docker-compose.yml", // shadowed: compose.yaml wins on precedence
		"deep/a/b/c/compose.yaml",
		"deep/a/b/c/d/compose.yaml", // one level too deep
		"compose.yaml",              // a stack that is the root itself
		".hidden/compose.yaml",      // not a stack anyone means
	} {
		e.write(filepath.Join(e.stacks(), rel), "services: {}\n", 0o644)
	}
	e.config(fmt.Sprintf("roots = [%q, %q]\n", e.stacks(), filepath.Join(e.root, "unmounted-drive")))

	got := e.run("discover")
	assertCode(t, "discover", got.code, 0, got)
	assertContains(t, "discover", got.stdout,
		"NAME", "STATE", "adguard", "available", "traefik", "both", "c", "srv")
	assertOmits(t, "discover", got.stdout, ".hidden", "c/d/compose.yaml", "unmounted-drive")
	// Compose's own precedence, so `discover` and `enroll` name the same file.
	assertContains(t, "discover", got.stdout, "both/compose.yaml")
	assertOmits(t, "discover", got.stdout, "both/docker-compose.yml")
}

func TestDiscoverMarksEnrolledStacks(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.write(filepath.Join(e.stacks(), "adguard", "compose.yaml"),
		fmt.Sprintf("services:\n  adguard:\n    image: %s\n", e.ref("app", "1.0.0")), 0o644)
	e.write(filepath.Join(e.stacks(), "media", "compose.yaml"), "services: {}\n", 0o644)
	e.config(fmt.Sprintf("roots = [%q]\n", e.stacks()))
	e.enroll(filepath.Join(e.stacks(), "adguard"))

	got := e.run("discover")
	assertCode(t, "discover", got.code, 0, got)
	assertContains(t, "discover", got.stdout, "adguard", "enrolled", "media", "available")
}

// A running Compose project is discoverable even with no roots configured:
// monitoring never begins implicitly, but finding out is not enrollment.
func TestDiscoverFindsRunningProjectsWithNoRootsConfigured(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Dir(e.stack("media", "services:\n  web:\n    image: app:1.0.0\n"))
	e.docker.running(container{Project: "media", WorkingDir: dir,
		ConfigFiles: filepath.Join(dir, "compose.yaml"), Service: "web"})

	got := e.run("discover")
	assertCode(t, "discover", got.code, 0, got)
	assertContains(t, "discover", e.normalize(got.stdout), "media", "available", filepath.Join("ROOT", "srv", "media", "compose.yaml"))

	enrolled := e.run("enroll", "--running", "media")
	assertCode(t, "enroll --running", enrolled.code, 0, enrolled)
	assertContains(t, "enroll", enrolled.stdout, "enrolled media")

	again := e.run("discover")
	assertContains(t, "discover", again.stdout, "media", "enrolled")
}

// A Compose label set stackmon cannot act on -- a relative path, a directory
// that is gone, no config file at all -- must not become a candidate.
func TestDiscoverIgnoresIncompleteDockerProjects(t *testing.T) {
	e := newEnv(t)
	gone := filepath.Join(e.root, "gone")
	e.docker.running(
		container{Project: "relative", WorkingDir: "relative", ConfigFiles: "relative/compose.yaml", Service: "web"},
		container{Project: "vanished", WorkingDir: gone, ConfigFiles: filepath.Join(gone, "compose.yaml"), Service: "web"},
		container{Project: "unlabelled", Service: "web"},
		container{Project: "notcompose", WorkingDir: e.stacks(), ConfigFiles: filepath.Join(e.stacks(), "compose.yaml")},
	)

	got := e.run("discover")
	assertCode(t, "discover", got.code, 0, got)
	assertOmits(t, "discover", got.stdout, "relative", "vanished", "unlabelled", "notcompose")
}

func TestEnrollByPathChecksTheDirectoryFirst(t *testing.T) {
	e := newEnv(t)
	empty := e.mkdir(filepath.Join(e.root, "empty"))

	got := e.run("enroll", empty)
	assertCode(t, "enroll a directory with no compose file", got.code, 1, got)
	assertContains(t, "stderr", got.stderr, "error:", "no compose file")
}

func TestEnrollRejectsADuplicateName(t *testing.T) {
	e := newEnv(t)
	e.stack("traefik", "services: {}\n")
	e.stack("edge", "services: {}\n")
	e.enroll(filepath.Join(e.stacks(), "traefik"))

	// Same basename in a different root: the collision has to surface at
	// enroll time, not as two stacks sharing one name later.
	e.write(filepath.Join(e.root, "other", "traefik", "compose.yaml"), "services: {}\n", 0o644)
	clash := e.run("enroll", filepath.Join(e.root, "other", "traefik"))
	assertCode(t, "enroll a duplicate name", clash.code, 1, clash)
	assertContains(t, "stderr", clash.stderr, "error:", "already enrolled", "--name")

	renamed := e.run("enroll", filepath.Join(e.root, "other", "traefik"), "--name", "edgebox")
	assertCode(t, "enroll with --name", renamed.code, 0, renamed)
	assertContains(t, "enroll", renamed.stdout, "enrolled edgebox")

	list := e.run("inventory", "list")
	assertCode(t, "inventory list", list.code, 0, list)
	assertContains(t, "inventory list", list.stdout, "NAME", "PATH", "ENROLLED", "traefik", "edgebox")
}

func TestEnrollRunningRejectsAmbiguousArguments(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Dir(e.stack("media", "services: {}\n"))
	e.docker.running(container{Project: "media", WorkingDir: dir, Service: "web"})

	both := e.run("enroll", dir, "--running", "media")
	assertCode(t, "enroll with a path and --running", both.code, 1, both)
	assertContains(t, "stderr", both.stderr, "error:", "--running cannot be combined with a path")

	aliased := e.run("enroll", "--running", "media", "--name", "alias")
	assertCode(t, "enroll --running with --name", aliased.code, 1, aliased)
	assertContains(t, "stderr", aliased.stderr, "error:", "--name cannot be combined with --running")

	unknown := e.run("enroll", "--running", "nosuchproject")
	assertCode(t, "enroll an unknown project", unknown.code, 1, unknown)
	assertContains(t, "stderr", unknown.stderr, "error:", "nosuchproject", "not found")
}

func TestEnrollRunningNeedsADaemon(t *testing.T) {
	e := newEnv(t)
	e.set("STACKMON_DOCKER_SOCKET", filepath.Join(e.root, "absent.sock"))

	got := e.run("enroll", "--running", "media")
	assertCode(t, "enroll --running without a daemon", got.code, 1, got)
	assertContains(t, "stderr", got.stderr, "error:", "Docker is unavailable")
}

func TestUnenrollRemovesAStack(t *testing.T) {
	e := newEnv(t)
	e.stack("demo", "services: {}\n")
	e.enroll(filepath.Join(e.stacks(), "demo"))

	got := e.run("unenroll", "demo")
	assertCode(t, "unenroll", got.code, 0, got)
	assertContains(t, "stdout", got.stdout, "unenrolled demo")

	again := e.run("unenroll", "demo")
	assertCode(t, "unenroll twice", again.code, 1, again)
	assertContains(t, "stderr", again.stderr, "error:", "not enrolled")

	assertOmits(t, "inventory list", e.run("inventory", "list").stdout, "demo")
}

func TestCheckNamesAStackThatIsNotEnrolled(t *testing.T) {
	e := newEnv(t)
	e.stack("demo", "services: {}\n")

	got := e.run("check", "ghost")
	assertCode(t, "check an unenrolled stack", got.code, 1, got)
	assertContains(t, "stderr", got.stderr, "error:", "ghost", "is not enrolled")
}

// Completion is how the commands get typed, so the suggestions are part of
// the contract: enrolled stacks for the first argument, that stack's services
// for the second.
func TestShellCompletionSuggestsStacksAndServices(t *testing.T) {
	e := newEnv(t)
	e.stack("alpha", "services:\n  web:\n    image: app:1.0.0\n  db:\n    image: pg:15\n")
	e.stack("beta", "services: {}\n")
	e.enroll(filepath.Join(e.stacks(), "alpha"))
	e.enroll(filepath.Join(e.stacks(), "beta"))

	stacks := e.run("__complete", "check", "")
	assertCode(t, "complete stacks", stacks.code, 0, stacks)
	assertContains(t, "complete stacks", stacks.stdout, "alpha", "beta", ":4")
	// A stack already named drops out, so check never suggests it twice.
	partial := e.run("__complete", "check", "alpha", "")
	assertOmits(t, "complete check alpha", partial.stdout, "alpha")
	assertContains(t, "complete check alpha", partial.stdout, "beta")

	services := e.run("__complete", "bump", "alpha", "")
	assertCode(t, "complete services", services.code, 0, services)
	assertContains(t, "complete services", services.stdout, "db", "web")

	unknown := e.run("__complete", "bump", "nosuchstack", "")
	assertCode(t, "complete services of an unknown stack", unknown.code, 0, unknown)
	assertOmits(t, "complete services of an unknown stack", unknown.stdout, "db")
}

func TestInventorySurvivesAFreshProcess(t *testing.T) {
	e := newEnv(t)
	stack := e.stack("demo", "services:\n  api:\n    image: app:1.0.0\n")
	e.enroll(filepath.Join(e.stacks(), "demo"))

	// The inventory is machine-owned JSON on disk; a rewritten run must not
	// lose or reorder what is already there.
	e.run("unenroll", "demo")
	if r := e.enroll(filepath.Dir(stack)); r.code != 0 {
		t.Fatalf("re-enrolling: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}

	raw := e.read(filepath.Join(e.home(), ".local", "state", "stackmon", "inventory.json"))
	assertContains(t, "inventory file", e.normalize(raw), `"version":1`, `"name":"demo"`,
		`"file":"compose.yaml"`, "ROOT/srv/demo")
	if strings.Count(raw, `"name"`) != 1 {
		t.Errorf("inventory holds %d stacks, want 1: %s", strings.Count(raw, `"name"`), raw)
	}
	entries, err := os.ReadDir(filepath.Join(e.home(), ".local", "state", "stackmon"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("state directory holds %v, want only inventory.json", names(entries))
	}
}

// A daemon that is present but failing is not a missing daemon: the error
// has to reach the user instead of silently producing an empty report.
func TestDiscoverReportsAFailingDaemon(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Dir(e.stack("media", "services: {}\n"))
	e.docker.running(container{Project: "media", WorkingDir: dir, Service: "web"})
	e.docker.fail()

	got := e.run("discover")
	assertCode(t, "discover", got.code, 1, got)
	assertContains(t, "stderr", got.stderr, "error:", "500")
}
