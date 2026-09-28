package e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// The enrolled name is a display name. Compose derives its project name from
// the directory, `name:`, or `-p`, and an enrolled alias is neither, so the
// only thing that identifies a project is the path Docker recorded on the
// containers it started.
func TestCheckMatchesAProjectByPathNotByTheEnrolledName(t *testing.T) {
	e := newEnv(t)
	declared := e.push("redis", "8.2", image{})
	stale := e.push("redis", "8.1", image{})
	file := e.stack("cache", fmt.Sprintf("services:\n  redis:\n    image: %s@%s\n", e.ref("redis", "8.2"), declared))
	dir := filepath.Dir(file)
	// `docker compose -p cache-prod` from the stack's own directory: a
	// project name that matches neither the directory nor the enrolled name.
	e.docker.running(container{Project: "cache-prod", WorkingDir: dir, ConfigFiles: file,
		Service: "redis", ImageID: "sha256:local", RepoDigests: []string{"redis@" + stale}})
	if r := e.run("enroll", dir, "--name", "edge-cache"); r.code != 0 {
		t.Fatalf("enrolling an alias: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}

	row := e.checkJSON().image(t, "edge-cache", "redis")
	assertEq(t, "project", row.Project, "cache-prod")
	assertEq(t, "project bound", row.ProjectBound, false)
	if !row.Running {
		t.Error("Running = false, want true: the project running this compose file is not the enrolled name")
	}
	assertEq(t, "status", row.Status, "not-deployed")
	assertOmits(t, "table", e.run("check").stdout, "not-running")
}

// A compose `name:` is a project name, not a path: it must not be treated as
// proof of identity either, and it must not stop the path match working.
func TestCheckMatchesAProjectThatOverridesItsNameInTheComposeFile(t *testing.T) {
	e := newEnv(t)
	serving := e.push("app", "latest", image{})
	file := e.stack("web", fmt.Sprintf("name: chosen-elsewhere\n\nservices:\n  api:\n    image: %s\n", e.ref("app", "latest")))
	e.docker.running(container{Project: "launched-as", WorkingDir: filepath.Dir(file), ConfigFiles: file,
		Service: "api", ImageID: "sha256:local", RepoDigests: []string{"app@" + serving}})
	if r := e.enroll(filepath.Dir(file)); r.code != 0 {
		t.Fatalf("enrolling: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}

	row := e.checkJSON().image(t, "web", "api")
	assertEq(t, "project", row.Project, "launched-as")
	assertEq(t, "status", row.Status, "current")
}

// A binding is what the user asserted. When the bound project has nothing
// running, that is a fact to report, not a reason to look for another project
// that happens to match the stack's paths.
func TestABindingIsNeverReplacedByAnAutomaticMatch(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{})
	body := fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared)
	file := e.enrollStack("demo", body)
	elsewhere := e.stack("elsewhere", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared))
	_ = file
	// The bound project is up, but not this service: reported as stopped for
	// this service, never as "let me look for another project instead".
	e.docker.running(
		container{Project: "demo", Service: "api", ImageID: "sha256:local", RepoDigests: []string{"app@" + declared}},
		container{Project: "elsewhere", WorkingDir: filepath.Dir(elsewhere), ConfigFiles: elsewhere, Service: "db", ImageID: "sha256:other"},
	)

	assertEq(t, "unbound", e.checkJSON().image(t, "demo", "api").Status, "current")

	bound := e.run("inventory", "set-project", "demo", "elsewhere")
	assertCode(t, "set-project", bound.code, 0, bound)
	assertContains(t, "set-project", bound.stdout, "bound demo to Compose project elsewhere")

	row := e.checkJSON().image(t, "demo", "api")
	assertEq(t, "project", row.Project, "elsewhere")
	assertEq(t, "project bound", row.ProjectBound, true)
	assertEq(t, "replicas", len(row.Replicas), 0)
	assertEq(t, "status", row.Status, "not-running")

	cleared := e.run("inventory", "set-project", "demo", "--clear")
	assertCode(t, "set-project --clear", cleared.code, 0, cleared)
	assertEq(t, "unbound again", e.checkJSON().image(t, "demo", "api").Status, "current")
}

// Two projects can serve the same directory -- `docker compose -p one` and
// `-p two` from one checkout, say. Which one the user means is their call, so
// stackmon says so and names the command that settles it.
func TestTwoProjectsOnOneEnrolledPathAreAmbiguousNotGuessed(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{})
	file := e.enrollStack("cache", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared))
	one := container{Project: "one", WorkingDir: filepath.Dir(file), ConfigFiles: file,
		Service: "api", ImageID: "sha256:one", RepoDigests: []string{"app@" + declared}}
	two := container{Project: "two", WorkingDir: filepath.Dir(file), ConfigFiles: file,
		Service: "api", ImageID: "sha256:two", RepoDigests: []string{"app@" + declared}}
	e.docker.running(one, two)

	row := e.checkJSON().image(t, "cache", "api")
	assertEq(t, "status", row.Status, "unknown")
	assertEq(t, "project", row.Project, "")
	assertContains(t, "identity note", row.IdentityNote, "one", "two", "set-project")
	if row.Status == "not-running" {
		t.Error("status = not-running, want unknown: the identity is ambiguous, not absent")
	}
	assertContains(t, "table", e.run("check").stdout, "unknown", "ambiguous", "one and two all run")

	// The detail view has to say the same thing, or the table row is the only
	// place a user can find out why.
	detail := e.run("show", "cache")
	assertCode(t, "show", detail.code, 0, detail)
	assertContains(t, "detail", detail.stdout, "note:", "ambiguous", "one and two all run", "set-project")

	if r := e.run("inventory", "set-project", "cache", "two"); r.code != 0 {
		t.Fatalf("binding the intended project: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	resolved := e.checkJSON().image(t, "cache", "api")
	assertEq(t, "resolved project", resolved.Project, "two")
	assertEq(t, "resolved status", resolved.Status, "current")
}

// Identity comes from paths, so a project that records none cannot be
// identified -- not even when its name is the enrolled name, which is exactly
// the coincidence the old matching relied on.
func TestAnUnlabelledProjectIsNotIdentifiedByItsName(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{})
	body := fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared)
	archived := e.write(filepath.Join(e.stacks(), "archive", "compose.yaml"), body, 0o644)
	// The running project is called cache and the stack is enrolled as cache,
	// but Docker recorded no directory for it: the name is a coincidence.
	e.docker.running(container{Project: "cache", Service: "api", ImageID: "sha256:local",
		RepoDigests: []string{"app@" + declared}})
	if r := e.run("enroll", filepath.Dir(archived), "--name", "cache"); r.code != 0 {
		t.Fatalf("enrolling: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}

	row := e.checkJSON().image(t, "cache", "api")
	assertEq(t, "status", row.Status, "unknown")
	assertEq(t, "project", row.Project, "")
	assertContains(t, "identity note", row.IdentityNote, "cache", "set-project")
	if row.Running {
		t.Error("Running = true, want false: the container's identity was never established")
	}

	if r := e.run("inventory", "set-project", "cache", "cache"); r.code != 0 {
		t.Fatalf("binding the project: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	bound := e.checkJSON().image(t, "cache", "api")
	assertEq(t, "bound project", bound.Project, "cache")
	assertEq(t, "bound status", bound.Status, "current")
}

// An inventory written before project bindings existed has no project field.
// It must keep working, and its display name must not be quietly promoted to
// a binding -- the whole point of matching by path.
func TestALegacyInventoryLoadsWithoutInventingABinding(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{})
	file := e.stack("legacy", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared))
	// A different project name serving the stack's path: name-based matching
	// would call this stopped, path-based matching finds it running.
	e.docker.running(container{Project: "renamed-upstream", WorkingDir: filepath.Dir(file), ConfigFiles: file,
		Service: "api", ImageID: "sha256:local", RepoDigests: []string{"app@" + declared}})

	inv := filepath.Join(e.home(), ".local", "state", "stackmon", "inventory.json")
	e.write(inv, fmt.Sprintf(
		`{"version":1,"stacks":[{"name":"legacy","dir":%q,"file":"compose.yaml","enrolled":"2026-01-01T00:00:00Z"}]}`,
		filepath.Dir(file)), 0o644)

	list := e.run("inventory", "list")
	assertCode(t, "inventory list", list.code, 0, list)
	assertContains(t, "inventory list", list.stdout, "legacy", "auto")

	row := e.checkJSON().image(t, "legacy", "api")
	assertEq(t, "project", row.Project, "renamed-upstream")
	assertEq(t, "project bound", row.ProjectBound, false)
	assertEq(t, "status", row.Status, "current")
	// The file on disk is untouched by reading it, and still has no binding
	// for stackmon to have invented.
	assertOmits(t, "inventory file", e.read(inv), `"project"`)
}

// Binding is how a user states what stackmon can only guess, so it is a
// first-class command: it must round-trip, leave the enrollment alone, and
// refuse arguments that contradict each other.
func TestBindingAProjectRoundTripsAndValidatesItsArguments(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared))
	raw := func() string { return e.read(filepath.Join(e.home(), ".local", "state", "stackmon", "inventory.json")) }
	before := raw()

	set := e.run("inventory", "set-project", "demo", "custom-project")
	assertCode(t, "set-project", set.code, 0, set)
	assertContains(t, "set-project", set.stdout, "bound demo to Compose project custom-project")
	after := raw()
	assertContains(t, "inventory", after, `"project":"custom-project"`)
	// The binding is a fact about this stack, not a re-enrollment of it.
	for _, kept := range []string{`"name":"demo"`, `"file":"compose.yaml"`, `"enrolled":"`} {
		assertContains(t, "preserved", after, kept)
	}
	if strings.Count(after, `"name"`) != 1 {
		t.Errorf("setting a project changed the stack count: %s", after)
	}
	assertOmits(t, "before", before, `"project"`)

	cleared := e.run("inventory", "set-project", "demo", "--clear")
	assertCode(t, "set-project --clear", cleared.code, 0, cleared)
	assertContains(t, "cleared", cleared.stdout, "cleared the project binding for demo")
	assertOmits(t, "inventory", raw(), `"project"`)
	for _, kept := range []string{`"name":"demo"`, `"file":"compose.yaml"`, `"enrolled":"`} {
		assertContains(t, "preserved", raw(), kept)
	}

	for _, bad := range []struct {
		args   []string
		reason string
	}{
		{[]string{"inventory", "set-project", "demo", "custom", "--clear"}, "--clear"},
		{[]string{"inventory", "set-project", "demo"}, "requires a stack and a project"},
		{[]string{"inventory", "set-project", "ghost", "custom"}, "not enrolled"},
		{[]string{"inventory", "set-project", "demo", "Not A Project"}, "not a valid Compose project name"},
		{[]string{"inventory", "set-project", "demo", "demo!"}, "not a valid Compose project name"},
	} {
		got := e.run(bad.args...)
		assertCode(t, strings.Join(bad.args, " "), got.code, 1, got)
		assertContains(t, "stderr", got.stderr, "error:", bad.reason)
	}
	assertOmits(t, "inventory", raw(), `"project"`)
}

// A path enrollment can state the binding up front, and a running enrollment
// records the project it was asked for.
func TestEnrollCanBindAProject(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{})
	file := e.stack("demo", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared))
	e.docker.running(container{Project: "running-project", WorkingDir: filepath.Dir(file), ConfigFiles: file,
		Service: "api", ImageID: "sha256:local", RepoDigests: []string{"app@" + declared}})

	if r := e.run("enroll", filepath.Dir(file), "--project", "running-project"); r.code != 0 {
		t.Fatalf("enroll --project: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	assertContains(t, "enroll", e.run("inventory", "list").stdout, "running-project")
	row := e.checkJSON().image(t, "demo", "api")
	assertEq(t, "project bound", row.ProjectBound, true)
	assertEq(t, "status", row.Status, "current")

	// The bound project is stored, so an unlabelled project is still found
	// once the user has said which one they meant.
	other := e.stack("other", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared))
	e.docker.running(container{Project: "silent", Service: "api", ImageID: "sha256:local",
		RepoDigests: []string{"app@" + declared}})
	if r := e.run("enroll", filepath.Dir(other), "--name", "other", "--project", "silent"); r.code != 0 {
		t.Fatalf("enrolling a second stack: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	bound := e.checkJSON().image(t, "other", "api")
	assertEq(t, "bound and unlabelled", bound.Project, "silent")
	assertEq(t, "bound and unlabelled status", bound.Status, "current")

	// --running already knows the project; saying it again is a contradiction.
	both := e.run("enroll", "--running", "running-project", "--project", "running-project")
	assertCode(t, "enroll --running --project", both.code, 1, both)
	assertContains(t, "stderr", both.stderr, "error:", "--project cannot be combined with --running")

	bad := e.run("enroll", filepath.Dir(file), "--name", "third", "--project", "Not A Project")
	assertCode(t, "enroll --project with an invalid name", bad.code, 1, bad)
	assertContains(t, "stderr", bad.stderr, "error:", "not a valid Compose project name")
}

// A running enrollment knows exactly which project it enrolled, and says so
// in the inventory, so a later check does not have to re-derive it.
func TestRunningEnrollmentStoresTheProjectItFound(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{})
	file := e.stack("media", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared))
	e.docker.running(container{Project: "media", WorkingDir: filepath.Dir(file), ConfigFiles: file,
		Service: "api", ImageID: "sha256:local", RepoDigests: []string{"app@" + declared}})

	enrolled := e.run("enroll", "--running", "media")
	assertCode(t, "enroll --running", enrolled.code, 0, enrolled)
	assertContains(t, "enroll", enrolled.stdout, "enrolled media", "bound to Compose project media")
	assertContains(t, "inventory", e.read(filepath.Join(e.home(), ".local", "state", "stackmon", "inventory.json")), `"project":"media"`)

	row := e.checkJSON().image(t, "media", "api")
	assertEq(t, "project bound", row.ProjectBound, true)
	assertEq(t, "status", row.Status, "current")
}

// The command has to be discoverable, or a user told to bind a project has to
// guess the spelling.
func TestBindingCommandsDocumentThemselves(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{})
	file := e.stack("demo", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared))
	e.docker.running(container{Project: "demo", WorkingDir: filepath.Dir(file), ConfigFiles: file, Service: "api"})
	if r := e.enroll(filepath.Dir(file)); r.code != 0 {
		t.Fatalf("enrolling: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}

	help := e.run("inventory", "set-project", "--help")
	assertCode(t, "set-project --help", help.code, 0, help)
	assertContains(t, "set-project help", help.stdout, "set-project", "--clear", "directory", "enrollment date")

	enrollHelp := e.run("enroll", "--help")
	assertCode(t, "enroll --help", enrollHelp.code, 0, enrollHelp)
	assertContains(t, "enroll help", enrollHelp.stdout, "--project", "--running", "--name")

	// The project being bound is a name the daemon knows, not one to recall.
	stacks := e.run("__complete", "inventory", "set-project", "")
	assertCode(t, "complete stacks for set-project", stacks.code, 0, stacks)
	assertContains(t, "complete stacks for set-project", stacks.stdout, "demo")

	projects := e.run("__complete", "inventory", "set-project", "demo", "")
	assertCode(t, "complete projects for set-project", projects.code, 0, projects)
	assertContains(t, "complete projects for set-project", projects.stdout, "demo", ":4")
}
