package e2e

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

type unmanagedRow struct {
	Project       string   `json:"project"`
	Containers    []string `json:"containers"`
	EnrolledStack string   `json:"enrolled_stack"`
	ComposePaths  []string `json:"compose_paths"`
	Reason        string   `json:"reason"`
}
type unmanagedReport struct {
	Stacks    []json.RawMessage `json:"stacks"`
	Unmanaged []unmanagedRow    `json:"unmanaged"`
	Unknown   []unmanagedRow    `json:"unmanaged_unknown"`
	Complete  bool              `json:"unmanaged_complete"`
	Error     string            `json:"unmanaged_error"`
}

func readUnmanaged(t *testing.T, r result) unmanagedReport {
	t.Helper()
	var report unmanagedReport
	if err := json.Unmarshal([]byte(r.stdout), &report); err != nil {
		t.Fatalf("invalid report: %v\n%s", err, r.stdout)
	}
	if report.Unmanaged == nil || report.Unknown == nil {
		t.Fatalf("requested lists must be arrays: %s", r.stdout)
	}
	return report
}

func TestDiscoverUnmanagedProjects(t *testing.T) {
	e := newEnv(t)
	path := e.stack("watched", "services: {}\n")
	bound := e.stack("bound", "services: {}\n")
	extra := e.stack("extra", "services: {}\n")
	e.enroll(filepath.Dir(path))
	binding := e.run("enroll", filepath.Dir(bound), "--project", "custom")
	assertCode(t, "bind", binding.code, 0, binding)
	invPath := filepath.Join(e.home(), ".local/state/stackmon/inventory.json")
	before := e.read(invPath)
	cs := []container{
		{Project: "actual", Service: "web", Name: "covered", WorkingDir: filepath.Dir(path)},
		{Project: "custom", Service: "web", Name: "bound", WorkingDir: "/elsewhere"},
		{Project: "extra", Service: "web", Name: "extra-b", ImageID: "never-inspect"},
		{Project: "extra", Service: "web", Name: "extra-a", ImageID: "never-inspect"},
		{Project: "gone", Service: "web", Name: "gone", WorkingDir: "/missing", ConfigFiles: "/missing/compose.yaml"},
		{Project: "job", Service: "web", Name: "job", OneOff: true},
		{Project: "stopped", Service: "web", Name: "stopped", State: "exited"},
	}
	e.docker.running(cs...)
	e.docker.reset()
	got := e.run("discover", "--unmanaged", "--json")
	assertCode(t, "unmanaged", got.code, 0, got)
	report := readUnmanaged(t, got)
	want := []unmanagedRow{{Project: "extra", Containers: []string{"extra-a", "extra-b"}, ComposePaths: []string{extra}}, {Project: "gone", Containers: []string{"gone"}, ComposePaths: []string{}}}
	if !reflect.DeepEqual(report.Unmanaged, want) || len(report.Unknown) != 0 || !report.Complete {
		t.Fatalf("unexpected report: %+v", report)
	}
	if after := e.read(invPath); before != after {
		t.Fatal("discovery mutated Inventory")
	}
	if n := e.docker.lookups(); n != 0 {
		t.Fatalf("discovery inspected %d images", n)
	}
	e.note("Inventory unchanged; Docker image requests: 0")
	for i, j := 0, len(cs)-1; i < j; i, j = i+1, j-1 {
		cs[i], cs[j] = cs[j], cs[i]
	}
	e.docker.running(cs...)
	again := e.run("discover", "--unmanaged", "--json")
	if got.stdout != again.stdout {
		t.Fatalf("Docker order changed output\n%s", again.stdout)
	}
	table := e.run("discover", "--unmanaged")
	assertCode(t, "table", table.code, 0, table)
	assertContains(t, "table", table.stdout, "Unmanaged Compose Projects", "extra-a", "gone", "none found")
}

func TestDiscoverUnmanagedUncertainCoverage(t *testing.T) {
	e := newEnv(t)
	path := e.stack("watched", "services: {}\n")
	e.enroll(filepath.Dir(path))
	e.docker.running(
		container{Project: "a", Service: "web", Name: "a", WorkingDir: filepath.Dir(path)},
		container{Project: "b", Service: "web", Name: "b", ConfigFiles: path},
		container{Project: "unlabelled", Service: "web", Name: "unlabelled"},
		container{Project: "other", Service: "web", Name: "other", WorkingDir: "/other"},
	)
	got := e.run("discover", "--unmanaged", "--json")
	assertCode(t, "uncertain", got.code, 0, got)
	report := readUnmanaged(t, got)
	if len(report.Unmanaged) != 1 || report.Unmanaged[0].Project != "other" || len(report.Unknown) != 3 {
		t.Fatalf("unexpected coverage: %+v", report)
	}
	for _, r := range report.Unknown {
		if r.Reason == "" {
			t.Fatal("missing uncertainty reason")
		}
	}
	table := e.run("discover", "--unmanaged")
	assertContains(t, "table", table.stdout, "Projects with uncertain enrollment coverage", "ambiguous", "unverifiable")
	// Conflicting evidence within a single Project is uncertainty too.
	e.docker.running(container{Project: "conflict", Service: "web", Name: "one", WorkingDir: filepath.Dir(path)}, container{Project: "conflict", Service: "web", Name: "two", WorkingDir: "/elsewhere"})
	got = e.run("discover", "--unmanaged", "--json")
	report = readUnmanaged(t, got)
	if len(report.Unmanaged) != 0 || len(report.Unknown) != 1 {
		t.Fatalf("conflict asserted unmanaged: %+v", report)
	}
	assertContains(t, "reason", report.Unknown[0].Reason, "conflicting")
}

func TestDiscoverUnmanagedEmptyAndFailedDocker(t *testing.T) {
	e := newEnv(t)
	got := e.run("discover", "--unmanaged", "--json")
	assertCode(t, "empty", got.code, 0, got)
	report := readUnmanaged(t, got)
	if !report.Complete || len(report.Unmanaged) != 0 || len(report.Unknown) != 0 {
		t.Fatalf("bad empty report: %+v", report)
	}
	// No enrolled Stacks means even a Project without path labels is unmanaged.
	e.docker.running(container{Project: "labels-only", Service: "web", Name: "labels-only"})
	got = e.run("discover", "--unmanaged", "--json")
	report = readUnmanaged(t, got)
	if len(report.Unmanaged) != 1 || len(report.Unmanaged[0].ComposePaths) != 0 {
		t.Fatalf("lost labels-only Project: %+v", report)
	}
	e.stack("available", "services: {}\n")
	e.config(fmt.Sprintf("roots = [%q]\n", e.stacks()))
	e.docker.fail()
	for _, jsonMode := range []bool{true, false} {
		args := []string{"discover", "--unmanaged"}
		if jsonMode {
			args = append(args, "--json")
		}
		got = e.run(args...)
		assertCode(t, "failed Docker", got.code, 1, got)
		assertContains(t, "error", got.stderr, "500")
		assertContains(t, "retained roots", got.stdout, "available")
		if jsonMode {
			report = readUnmanaged(t, got)
			if report.Complete || report.Error == "" {
				t.Fatal("Docker failure looks complete")
			}
		}
	}
	e.set("STACKMON_DOCKER_SOCKET", filepath.Join(e.root, "absent.sock"))
	got = e.run("discover", "--unmanaged", "--json")
	assertCode(t, "unavailable Docker", got.code, 1, got)
	report = readUnmanaged(t, got)
	if report.Complete || report.Error == "" {
		t.Fatal("absent Docker looks complete")
	}
}
