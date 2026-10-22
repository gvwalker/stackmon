package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
)

// Run from an unrelated directory without changing the test process's cwd.
func enrollRunIn(e *env, dir string, args ...string) result {
	e.t.Helper()
	cmd := e.command(nil, devBinary, args...)
	cmd.Dir = dir
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	r, err := outcome(args, cmd.Run(), out.String(), errOut.String())
	if err != nil {
		e.t.Fatal(err)
	}
	e.record(args, r)
	return r
}

func assertEnrollment(t *testing.T, e *env, name, file, project string) {
	t.Helper()
	var doc struct {
		Stacks []struct{ Name, Dir, File, Project string }
	}
	if err := json.Unmarshal([]byte(e.inventoryJSON()), &doc); err != nil {
		t.Fatal(err)
	}
	for _, s := range doc.Stacks {
		if s.Name == name {
			if s.Dir != filepath.Dir(file) || s.File != filepath.Base(file) || s.Project != project {
				t.Fatalf("wrong enrollment: %+v; want %s, project %q", s, file, project)
			}
			e.note("inventory: name=%s file=%s project=%q", name, e.normalize(filepath.Join(s.Dir, s.File)), s.Project)
			return
		}
	}
	t.Fatalf("%s missing from inventory: %s", name, e.inventoryJSON())
}

func TestEnrollDiscoveredName(t *testing.T) {
	e := newEnv(t)
	cwd := e.mkdir(filepath.Join(e.root, "unrelated"))
	file := e.stack("zennotes", "services: {}\n")
	e.stack("untouched", "services: {}\n")
	e.config(fmt.Sprintf("roots = [%q]\n", e.stacks()))
	discovered := enrollRunIn(e, cwd, "discover")
	assertCode(t, "discover", discovered.code, 0, discovered)
	assertContains(t, "discover", discovered.stdout, "zennotes", file)
	if e.inventoryJSON() != "" {
		t.Fatal("discovery changed inventory")
	}
	got := enrollRunIn(e, cwd, "enroll", "zennotes")
	assertCode(t, "enroll discovered name", got.code, 0, got)
	assertEnrollment(t, e, "zennotes", file, "")
	assertEqSlice(t, "only selected stack enrolled", e.enrolledNames(), []string{"zennotes"})
}

func TestEnrollDiscoveredNamePreservesDockerFileAndOverrides(t *testing.T) {
	e := newEnv(t)
	cwd := e.mkdir(filepath.Join(e.root, "unrelated"))
	dir := filepath.Join(e.root, "declarations")
	file := e.write(filepath.Join(dir, "production.yml"), "services: {}\n", 0o644)
	// A standard file beside the Docker declaration must not replace it.
	e.write(filepath.Join(dir, "compose.yaml"), "services: {}\n", 0o644)
	e.docker.running(container{Project: "running-project", WorkingDir: dir, ConfigFiles: file, Service: "web"})
	discovered := enrollRunIn(e, cwd, "discover")
	assertCode(t, "discover Docker-only stack", discovered.code, 0, discovered)
	assertContains(t, "discover", discovered.stdout, "running-project", file)
	for _, tc := range []struct {
		name, project string
		args          []string
	}{
		{"running-project", "", []string{"enroll", "running-project"}},
		{"display", "", []string{"enroll", "running-project", "--name", "display"}},
		{"bound", "explicit-project", []string{"enroll", "running-project", "--name", "bound", "--project", "explicit-project"}},
		{"running-project", "running-project", []string{"enroll", "--running", "running-project"}},
	} {
		got := enrollRunIn(e, cwd, tc.args...)
		assertCode(t, "enroll Docker declaration", got.code, 0, got)
		assertEnrollment(t, e, tc.name, file, tc.project)
		removed := e.run("unenroll", tc.name)
		assertCode(t, "reset enrollment", removed.code, 0, removed)
	}
}

func TestEnrollDiscoveredNameDirectoryPrecedence(t *testing.T) {
	e := newEnv(t)
	cwd := e.mkdir(filepath.Join(e.root, "unrelated"))
	e.stack("local", "services: {}\n")
	e.stack("empty", "services: {}\n")
	e.stack("missing", "services: {}\n")
	e.config(fmt.Sprintf("roots = [%q]\n", e.stacks()))
	local := e.write(filepath.Join(cwd, "local", "compose.yml"), "services: {}\n", 0o644)
	e.mkdir(filepath.Join(cwd, "empty"))
	for i, arg := range []string{"local", "./local", filepath.Dir(local)} {
		name := fmt.Sprintf("local-%d", i)
		got := enrollRunIn(e, cwd, "enroll", arg, "--name", name)
		assertCode(t, "enroll existing directory", got.code, 0, got)
		assertEnrollment(t, e, name, local, "")
	}
	before := e.inventoryJSON()
	for _, arg := range []string{"empty", "./missing", "missing/", "sub/../missing", filepath.Join(cwd, "missing")} {
		got := enrollRunIn(e, cwd, "enroll", arg)
		assertCode(t, "directory error without discovery fallback", got.code, 1, got)
		assertContains(t, "directory error", got.stderr, "no compose file")
		if e.inventoryJSON() != before {
			t.Fatal("failed directory enrollment changed inventory")
		}
	}
	e.note("failed directory enrollments left inventory unchanged")
}

func TestEnrollDiscoveredNameRejectsAmbiguousAndUnknown(t *testing.T) {
	e := newEnv(t)
	cwd := e.mkdir(filepath.Join(e.root, "unrelated"))
	seed := e.stack("seed", "services: {}\n")
	seeded := e.enroll(filepath.Dir(seed))
	assertCode(t, "seed inventory", seeded.code, 0, seeded)
	first := e.write(filepath.Join(e.stacks(), "one", "duplicate", "compose.yml"), "services: {}\n", 0o644)
	second := e.write(filepath.Join(e.stacks(), "two", "duplicate", "compose.yaml"), "services: {}\n", 0o644)
	e.config(fmt.Sprintf("roots = [%q]\n", e.stacks()))
	before := e.inventoryJSON()
	discovered := enrollRunIn(e, cwd, "discover")
	assertContains(t, "duplicate discovery", discovered.stdout, first, second)
	got := enrollRunIn(e, cwd, "enroll", "duplicate")
	assertCode(t, "ambiguous discovered name", got.code, 1, got)
	assertContains(t, "ambiguity guidance", got.stderr, "ambiguous", "enroll", filepath.Dir(first), filepath.Dir(second))
	unknown := enrollRunIn(e, cwd, "enroll", "unknown")
	assertCode(t, "unknown discovered name", unknown.code, 1, unknown)
	assertContains(t, "unknown guidance", unknown.stderr, "unknown", "discover", "enroll", "--running")
	if e.inventoryJSON() != before {
		t.Fatal("failed name enrollment changed inventory")
	}
	e.note("ambiguous and unknown names left inventory unchanged")
}

func TestEnrollDiscoveredNameUsesDiscoveryMerge(t *testing.T) {
	e := newEnv(t)
	cwd := e.mkdir(filepath.Join(e.root, "unrelated"))
	file := e.stack("root-name", "services: {}\n")
	e.config(fmt.Sprintf("roots = [%q, %q]\n", e.stacks(), e.stacks()))
	e.docker.running(container{Project: "different-project", WorkingDir: filepath.Dir(file), ConfigFiles: file, Service: "web"})
	discovered := enrollRunIn(e, cwd, "discover")
	assertContains(t, "root name wins discovery merge", discovered.stdout, "root-name")
	assertOmits(t, "root name wins discovery merge", discovered.stdout, "different-project")
	got := enrollRunIn(e, cwd, "enroll", "root-name")
	assertCode(t, "merged discovered name", got.code, 0, got)
	assertEnrollment(t, e, "root-name", file, "")
}

func TestEnrollDiscoveredNameDockerAvailability(t *testing.T) {
	for _, available := range []bool{false, true} {
		t.Run(fmt.Sprintf("daemon-present-%t", available), func(t *testing.T) {
			e := newEnv(t)
			cwd := e.mkdir(filepath.Join(e.root, "unrelated"))
			file := e.stack("demo", "services: {}\n")
			e.config(fmt.Sprintf("roots = [%q]\n", e.stacks()))
			if available {
				e.docker.fail()
				got := enrollRunIn(e, cwd, "enroll", "demo")
				assertCode(t, "failed discovery must not select a partial match", got.code, 1, got)
				assertContains(t, "Docker error", got.stderr, "500")
				if e.inventoryJSON() != "" {
					t.Fatal("failed discovery created inventory")
				}
			} else {
				e.set("STACKMON_DOCKER_SOCKET", filepath.Join(e.root, "absent.sock"))
				got := enrollRunIn(e, cwd, "enroll", "demo")
				assertCode(t, "root discovery without Docker", got.code, 0, got)
				assertEnrollment(t, e, "demo", file, "")
			}
		})
	}
}

func TestEnrollDiscoveredNameHelp(t *testing.T) {
	e := newEnv(t)
	got := e.run("enroll", "--help")
	assertCode(t, "enroll help", got.code, 0, got)
	assertContains(t, "enroll help", got.stdout, "discovered-name", "existing directory", "./name", "--running", "--project")
}

func TestEnrollDiscoveredNameRelativeRootPersistsAbsoluteDirectory(t *testing.T) {
	e := newEnv(t)
	cwd := e.mkdir(filepath.Join(e.root, "unrelated"))
	file := e.stack("demo", "services: {}\n")
	e.config("roots = ['../srv']\n")
	got := enrollRunIn(e, cwd, "enroll", "demo")
	assertCode(t, "enroll from relative root", got.code, 0, got)
	assertEnrollment(t, e, "demo", file, "")
}
