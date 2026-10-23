package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDiscoverJSONMatchesTable(t *testing.T) {
	e := newEnv(t)
	alpha := e.stack("alpha", "services: {}\n")
	// Discovery lists declarations without parsing YAML.
	broken := e.stack("broken", "services: [\n")
	other := filepath.Join(e.root, "other", "alpha", "docker-compose.yml")
	e.write(other, "services: {}\n", 0o644)
	running := filepath.Join(e.root, "running", "custom.yml")
	e.write(running, "services: {}\n", 0o644)
	e.docker.running(container{Project: "zeta", WorkingDir: filepath.Dir(running), ConfigFiles: running, Service: "web"})
	e.config(fmt.Sprintf("roots = [%q, %q]\n", e.stacks(), filepath.Join(e.root, "other")))
	e.enroll(filepath.Dir(alpha))
	table := e.run("discover")
	assertCode(t, "table", table.code, 0, table)
	got := e.run("discover", "--json")
	assertCode(t, "JSON", got.code, table.code, got)
	type row struct {
		Name     string `json:"name"`
		Path     string `json:"path"`
		Enrolled bool   `json:"enrolled"`
		State    string `json:"state"`
	}
	var report struct {
		Stacks []row `json:"stacks"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &report); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, got.stdout)
	}
	want := []row{{"alpha", other, false, "available"}, {"alpha", alpha, true, "enrolled"}, {"broken", broken, false, "available"}, {"zeta", running, false, "available"}}
	if !reflect.DeepEqual(report.Stacks, want) {
		t.Fatalf("stacks = %+v, want %+v", report.Stacks, want)
	}
	lines := strings.Split(strings.TrimSpace(table.stdout), "\n")[1:]
	if len(lines) != len(want) {
		t.Fatalf("table rows = %d, want %d", len(lines), len(want))
	}
	for i, r := range report.Stacks {
		if fields := strings.Fields(lines[i]); !reflect.DeepEqual(fields, []string{r.Name, r.State, r.Path}) {
			t.Errorf("table/JSON row %d differs: %v vs %+v", i, fields, r)
		}
	}
	var raw struct {
		Stacks []map[string]json.RawMessage `json:"stacks"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &raw); err != nil {
		t.Fatal(err)
	}
	for _, r := range raw.Stacks {
		if len(r) != 4 {
			t.Fatalf("expected all four fields: %v", r)
		}
	}
	if got.stderr != "" {
		t.Fatalf("unexpected stderr: %s", got.stderr)
	}
	again := e.run("discover", "--json")
	if got.stdout != again.stdout || got.stderr != again.stderr || got.code != again.code {
		t.Fatal("discovery JSON changed between identical runs")
	}
}

func TestDiscoverJSONEmptyAndUnavailableDocker(t *testing.T) {
	e := newEnv(t)
	e.set("STACKMON_DOCKER_SOCKET", filepath.Join(e.root, "absent.sock"))
	got := e.run("discover", "--json")
	assertCode(t, "empty JSON", got.code, 0, got)
	if strings.Join(strings.Fields(got.stdout), "") != `{"stacks":[]}` {
		t.Fatalf("empty result = %q", got.stdout)
	}
	path := e.stack("app", "services: {}\n")
	e.config(fmt.Sprintf("roots = [%q]\n", e.stacks()))
	got = e.run("discover", "--json")
	assertCode(t, "roots without Docker", got.code, 0, got)
	assertContains(t, "JSON", got.stdout, path, `"enrolled": false`, `"state": "available"`)
}

func TestDiscoverJSONPreservesErrors(t *testing.T) {
	e := newEnv(t)
	e.docker.fail()
	for _, args := range [][]string{{"discover"}, {"discover", "--json"}} {
		got := e.run(args...)
		assertCode(t, "failing Docker", got.code, 1, got)
		assertContains(t, "stderr", got.stderr, "error:", "500")
		if got.stdout != "" {
			t.Fatalf("error leaked to stdout: %q", got.stdout)
		}
	}
	e.config("roots = [\n")
	for _, args := range [][]string{{"discover"}, {"discover", "--json"}} {
		got := e.run(args...)
		assertCode(t, "invalid config", got.code, 1, got)
		assertContains(t, "stderr", got.stderr, "error:", "config: parsing")
		if got.stdout != "" {
			t.Fatalf("error leaked to stdout: %q", got.stdout)
		}
	}
}

func TestDiscoverJSONResolvesRelativeRoots(t *testing.T) {
	e := newEnv(t)
	path := e.stack("relative", "services: {}\n")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Rel(cwd, e.stacks())
	if err != nil {
		t.Fatal(err)
	}
	e.config(fmt.Sprintf("roots = [%q]\n", root))
	got := e.run("discover", "--json")
	assertCode(t, "relative roots", got.code, 0, got)
	assertContains(t, "absolute Compose path", got.stdout, path)
}
