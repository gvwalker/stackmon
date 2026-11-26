package e2e

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// compactHealthReport and compactHealthService hold the exact contract of
// `stackmon health --compact`. Key-set assertions against the raw JSON keep
// every promised field always present and catch accidental additions.
type compactHealthReport struct {
	DockerAvailable bool                   `json:"docker_available"`
	DockerError     string                 `json:"docker_error"`
	Services        []compactHealthService `json:"services"`
	StackFailures   []healthStackFailure   `json:"stack_failures"`
}

type compactHealthService struct {
	Stack               string `json:"stack"`
	Service             string `json:"service"`
	Container           string `json:"container"`
	Status              string `json:"status"`
	Health              string `json:"health"`
	Replicas            int    `json:"replicas"`
	DisagreeingReplicas int    `json:"disagreeing_replicas"`
	ReplicaDetail       string `json:"replica_detail"`
	Err                 string `json:"error"`
	IdentityNote        string `json:"identity_note"`
}

type healthStackFailure struct {
	Stack  string `json:"stack"`
	Reason string `json:"reason"`
}

func (e *env) healthCompact(args ...string) (compactHealthReport, result) {
	e.t.Helper()
	full := append([]string{"health", "--compact"}, args...)
	r := e.run(full...)
	var rep compactHealthReport
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
		e.t.Fatalf("health --compact: invalid JSON: %v\nstdout: %s\nstderr: %s", err, r.stdout, r.stderr)
	}
	return rep, r
}

func (rep compactHealthReport) service(t *testing.T, stack, service string) compactHealthService {
	t.Helper()
	for _, s := range rep.Services {
		if s.Stack == stack && s.Service == service {
			return s
		}
	}
	t.Fatalf("no health row for %s/%s in report: %+v", stack, service, rep.Services)
	return compactHealthService{}
}

func healthCompactKeys(t *testing.T, stdout string) []string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("health --compact: %v\n%s", err, stdout)
	}
	return slices.Sorted(maps.Keys(doc))
}

func healthCompactServiceKeys(t *testing.T, stdout string) []string {
	t.Helper()
	var doc struct {
		Services []map[string]any `json:"services"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("health --compact: %v\n%s", err, stdout)
	}
	if len(doc.Services) == 0 {
		t.Fatalf("health --compact: no service rows in\n%s", stdout)
	}
	return slices.Sorted(maps.Keys(doc.Services[0]))
}

// Health covers every declared Service of every enrolled Stack — image-backed,
// build-only, inherited, with or without a healthcheck — without ever calling a
// registry or inspecting image digests.
func TestHealthReportsHealthyNoHealthcheckBuildOnlyAndInheritedOffline(t *testing.T) {
	e := newEnv(t)
	e.enrollStack("demo", ""+
		"x-base: &base\n"+
		"  image: 127.0.0.1:1/unreachable-base:1.0.0\n"+
		"services:\n"+
		"  api:\n"+
		"    image: 127.0.0.1:1/unreachable-api:1.0.0\n"+
		"  web:\n"+
		"    image: 127.0.0.1:1/unreachable-web:1.0.0\n"+
		"  builder:\n"+
		"    build: .\n"+
		"  worker:\n"+
		"    <<: *base\n")

	e.docker.running(
		container{Project: "demo", Service: "api", Name: "demo-api-1", State: "running", Health: "healthy", ImageID: "sha256:api"},
		container{Project: "demo", Service: "web", Name: "demo-web-1", State: "running", Health: "", ImageID: "sha256:web"},
		container{Project: "demo", Service: "builder", Name: "demo-builder-1", State: "running", Health: "", ImageID: "sha256:builder"},
		container{Project: "demo", Service: "worker", Name: "demo-worker-1", State: "running", Health: "healthy", ImageID: "sha256:worker"},
	)

	e.hits.reset()
	e.docker.reset()

	rep, compact := e.healthCompact()
	assertCode(t, "health --compact", compact.code, 0, compact)
	assertEq(t, "stderr", compact.stderr, "")
	assertEq(t, "docker_available", rep.DockerAvailable, true)
	assertEq(t, "docker_error", rep.DockerError, "")
	assertEq(t, "service count", len(rep.Services), 4)
	assertEq(t, "stack_failures count", len(rep.StackFailures), 0)

	// Every promised compact key is always present.
	assertEqSlice(t, "top-level keys", healthCompactKeys(t, compact.stdout),
		[]string{"docker_available", "docker_error", "services", "stack_failures"})
	assertEqSlice(t, "service keys", healthCompactServiceKeys(t, compact.stdout),
		[]string{"container", "disagreeing_replicas", "error", "health", "identity_note", "replica_detail", "replicas", "service", "stack", "status"})

	api := rep.service(t, "demo", "api")
	assertEq(t, "api container", api.Container, "demo-api-1")
	assertEq(t, "api status", api.Status, "running")
	assertEq(t, "api health", api.Health, "healthy")
	assertEq(t, "api replicas", api.Replicas, 1)
	assertEq(t, "api disagreeing", api.DisagreeingReplicas, 0)
	assertEq(t, "api error", api.Err, "")
	assertEq(t, "api identity_note", api.IdentityNote, "")

	web := rep.service(t, "demo", "web")
	assertEq(t, "web container", web.Container, "demo-web-1")
	assertEq(t, "web status", web.Status, "running")
	assertEq(t, "web health", web.Health, "none")

	builder := rep.service(t, "demo", "builder")
	assertEq(t, "builder container", builder.Container, "demo-builder-1")
	assertEq(t, "builder status", builder.Status, "running")
	assertEq(t, "builder health", builder.Health, "none")

	worker := rep.service(t, "demo", "worker")
	assertEq(t, "worker container", worker.Container, "demo-worker-1")
	assertEq(t, "worker status", worker.Status, "running")
	assertEq(t, "worker health", worker.Health, "healthy")

	// Health must never contact any registry or fetch image RepoDigests.
	e.note("registry requests during health: %d; docker image lookups: %d", e.hits.count(""), e.docker.lookups())
	assertEq(t, "registry requests", e.hits.count(""), 0)
	assertEq(t, "docker image lookups", e.docker.lookups(), 0)

	// Table output and --fail-on-unhealthy both succeed when everything is
	// running and healthy (or running with no healthcheck).
	table := e.run("health")
	assertCode(t, "health table", table.code, 0, table)
	assertContains(t, "table", table.stdout, "STACK", "SERVICE", "CONTAINER", "STATUS", "HEALTH",
		"demo-api-1", "healthy", "demo-web-1", "none", "demo-builder-1", "demo-worker-1")

	gate := e.run("health", "--fail-on-unhealthy")
	assertCode(t, "health --fail-on-unhealthy all good", gate.code, 0, gate)
	assertEq(t, "stderr", gate.stderr, "")

	// Deterministic across repeated runs for the same state.
	_, again := e.healthCompact()
	assertEq(t, "deterministic compact output", again.stdout, compact.stdout)

	// Unknown stack name is a hard error (exit 1).
	bad := e.run("health", "nonexistent")
	assertCode(t, "health unknown stack", bad.code, 1, bad)
	assertContains(t, "stderr", bad.stderr, "error:", "not enrolled")

	// --json emits the full report including replicas and project metadata.
	full := e.exec(devBinary, "health", "--json")
	assertCode(t, "health --json", full.code, 0, full)
	assertContains(t, "health --json", full.stdout, `"docker_available": true`, `"project": "demo"`, `"demo-api-1"`)
}

// Health distinguishes liveness from healthcheck state across running,
// unhealthy, starting, exited, created, restarting, and paused containers,
// excludes one-off `docker compose run` jobs, and fails --fail-on-unhealthy
// with exit 2 for every non-good state.
func TestHealthReportsUnhealthyStartingStoppedAndExcludesOneOffs(t *testing.T) {
	e := newEnv(t)
	e.enrollStack("demo", ""+
		"services:\n"+
		"  unhealthy:\n"+
		"    image: app:1\n"+
		"  starting:\n"+
		"    image: app:1\n"+
		"  exited:\n"+
		"    image: app:1\n"+
		"  created:\n"+
		"    image: app:1\n"+
		"  restarting:\n"+
		"    image: app:1\n"+
		"  paused:\n"+
		"    image: app:1\n"+
		"  job:\n"+
		"    image: app:1\n")

	e.docker.running(
		container{Project: "demo", Service: "unhealthy", Name: "demo-unhealthy-1", State: "running", Health: "unhealthy"},
		container{Project: "demo", Service: "starting", Name: "demo-starting-1", State: "running", Health: "starting"},
		container{Project: "demo", Service: "exited", Name: "demo-exited-1", State: "exited", Health: ""},
		container{Project: "demo", Service: "created", Name: "demo-created-1", State: "created", Health: ""},
		container{Project: "demo", Service: "restarting", Name: "demo-restarting-1", State: "restarting", Health: ""},
		container{Project: "demo", Service: "paused", Name: "demo-paused-1", State: "paused", Health: ""},
		container{Project: "demo", Service: "job", Name: "demo-job-run-1", State: "running", Health: "healthy", OneOff: true},
	)

	rep, res := e.healthCompact()
	assertCode(t, "health --compact default exit", res.code, 0, res)

	want := map[string][3]string{
		"unhealthy":  {"demo-unhealthy-1", "running", "unhealthy"},
		"starting":   {"demo-starting-1", "running", "starting"},
		"exited":     {"demo-exited-1", "exited", "none"},
		"created":    {"demo-created-1", "created", "none"},
		"restarting": {"demo-restarting-1", "restarting", "none"},
		"paused":     {"demo-paused-1", "paused", "none"},
		"job":        {"", "absent", "none"},
	}
	for svc, w := range want {
		row := rep.service(t, "demo", svc)
		assertEq(t, svc+" container", row.Container, w[0])
		assertEq(t, svc+" status", row.Status, w[1])
		assertEq(t, svc+" health", row.Health, w[2])
	}

	// Each non-good state on its own must make --fail-on-unhealthy exit 2.
	for _, tc := range []struct {
		name   string
		state  string
		health string
	}{
		{"unhealthy", "running", "unhealthy"},
		{"starting", "running", "starting"},
		{"exited", "exited", ""},
		{"created", "created", ""},
		{"restarting", "restarting", ""},
		{"paused", "paused", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sub := newEnv(t)
			sub.enrollStack("s", "services:\n  app:\n    image: app:1\n")
			sub.docker.running(container{Project: "s", Service: "app", Name: "s-app-1", State: tc.state, Health: tc.health})
			gate := sub.run("health", "--fail-on-unhealthy")
			assertCode(t, tc.name+" --fail-on-unhealthy", gate.code, 2, gate)
			assertOmits(t, "stderr", gate.stderr, "error:")
		})
	}
}

// A confirmed absent container is reported as status=absent, health=none —
// never unknown — and fails --fail-on-unhealthy with exit 2.
func TestHealthReportsConfirmedAbsenceDistinctFromUnknown(t *testing.T) {
	e := newEnv(t)
	e.enrollStack("demo", "services:\n  api:\n    image: app:1\n  worker:\n    image: app:1\n")
	e.enrollStack("idle", "services:\n  web:\n    image: app:1\n")
	other := e.stack("other", "services: {}\n")

	e.docker.running(
		container{Project: "demo", Service: "api", Name: "demo-api-1", State: "running", Health: "healthy"},
		container{Project: "other", Service: "svc", Name: "other-svc-1", WorkingDir: filepath.Dir(other), ConfigFiles: other, State: "running"},
	)

	rep, _ := e.healthCompact()
	worker := rep.service(t, "demo", "worker")
	assertEq(t, "worker status", worker.Status, "absent")
	assertEq(t, "worker health", worker.Health, "none")
	assertEq(t, "worker container", worker.Container, "")
	assertEq(t, "worker error", worker.Err, "")
	assertEq(t, "worker identity_note", worker.IdentityNote, "")

	idle := rep.service(t, "idle", "web")
	assertEq(t, "idle status", idle.Status, "absent")
	assertEq(t, "idle health", idle.Health, "none")
	assertEq(t, "idle container", idle.Container, "")
	assertEq(t, "idle error", idle.Err, "")
	assertEq(t, "idle identity_note", idle.IdentityNote, "")

	gate := e.run("health", "--fail-on-unhealthy")
	assertCode(t, "absent --fail-on-unhealthy", gate.code, 2, gate)
}

// A missing socket, a failing daemon, and a failed per-container inspect all
// surface as unknown (never healthy, never absent) and cannot exit 0 under
// --fail-on-unhealthy.
func TestHealthReportsDockerAbsentFailingDaemonAndFailedInspectAsUnknown(t *testing.T) {
	t.Run("absent_socket", func(t *testing.T) {
		e := newEnv(t)
		e.set("STACKMON_DOCKER_SOCKET", filepath.Join(e.root, "absent.sock"))
		e.enrollStack("demo", "services:\n  api:\n    image: app:1\n")

		rep, res := e.healthCompact()
		assertCode(t, "health --compact absent socket", res.code, 0, res)
		assertEq(t, "docker_available", rep.DockerAvailable, false)
		assertEq(t, "docker_error", rep.DockerError, "")
		api := rep.service(t, "demo", "api")
		assertEq(t, "status", api.Status, "unknown")
		assertEq(t, "health", api.Health, "unknown")
		assertContains(t, "error", api.Err, "docker socket unavailable")

		table := e.run("health")
		assertCode(t, "health table absent socket", table.code, 0, table)
		assertContains(t, "table", table.stdout, "docker socket unavailable", "unknown")

		gate := e.run("health", "--fail-on-unhealthy")
		assertCode(t, "absent socket --fail-on-unhealthy", gate.code, 2, gate)
	})

	t.Run("failing_daemon", func(t *testing.T) {
		e := newEnv(t)
		e.docker.fail()
		e.enrollStack("demo", "services:\n  api:\n    image: app:1\n")

		rep, res := e.healthCompact()
		assertCode(t, "health --compact failing daemon", res.code, 0, res)
		assertEq(t, "docker_available", rep.DockerAvailable, false)
		assertContains(t, "docker_error", rep.DockerError, "500")
		api := rep.service(t, "demo", "api")
		assertEq(t, "status", api.Status, "unknown")
		assertEq(t, "health", api.Health, "unknown")
		assertContains(t, "error", api.Err, "500")

		gate := e.run("health", "--fail-on-unhealthy")
		assertCode(t, "failing daemon --fail-on-unhealthy", gate.code, 2, gate)
	})

	t.Run("failed_container_inspect", func(t *testing.T) {
		e := newEnv(t)
		e.enrollStack("demo", "services:\n  api:\n    image: app:1\n  db:\n    image: pg:16\n")
		e.docker.running(
			container{Project: "demo", Service: "api", Name: "demo-api-1", State: "running", Health: "healthy", FailInspect: true},
			container{Project: "demo", Service: "db", Name: "demo-db-1", State: "running", Health: "healthy"},
		)

		rep, _ := e.healthCompact()
		assertEq(t, "docker_available", rep.DockerAvailable, true)
		api := rep.service(t, "demo", "api")
		assertEq(t, "api container", api.Container, "demo-api-1")
		assertEq(t, "api status", api.Status, "unknown")
		assertEq(t, "api health", api.Health, "unknown")
		assertContains(t, "api error", api.Err, "500")

		db := rep.service(t, "demo", "db")
		assertEq(t, "db status", db.Status, "running")
		assertEq(t, "db health", db.Health, "healthy")

		gate := e.run("health", "--fail-on-unhealthy")
		assertCode(t, "failed inspect --fail-on-unhealthy", gate.code, 2, gate)
	})
}

// Ambiguous or unverifiable Project identity yields unknown + identity_note
// (never a guessed Service), fails --fail-on-unhealthy, and is settled by an
// explicit Project Binding.
func TestHealthReportsAmbiguousAndUnverifiableIdentityAndHonoursBinding(t *testing.T) {
	e := newEnv(t)
	composePath := e.enrollStack("demo", "services:\n  api:\n    image: app:1\n")
	dir := filepath.Dir(composePath)

	// Two projects both claiming the same compose file: ambiguous.
	e.docker.running(
		container{Project: "blue", WorkingDir: dir, ConfigFiles: composePath, Service: "api", Name: "blue-api-1", State: "running", Health: "healthy"},
		container{Project: "green", WorkingDir: dir, ConfigFiles: composePath, Service: "api", Name: "green-api-1", State: "running", Health: "unhealthy"},
	)

	rep, _ := e.healthCompact()
	api := rep.service(t, "demo", "api")
	assertEq(t, "ambiguous status", api.Status, "unknown")
	assertEq(t, "ambiguous health", api.Health, "unknown")
	assertEq(t, "ambiguous container", api.Container, "")
	assertEq(t, "ambiguous error", api.Err, "")
	assertContains(t, "identity_note", api.IdentityNote, "ambiguous", "blue", "green", "set-project")
	assertCode(t, "ambiguous --fail-on-unhealthy", e.run("health", "--fail-on-unhealthy").code, 2, result{})

	// Explicit Project Binding settles it immediately.
	bind := e.run("inventory", "set-project", "demo", "blue")
	assertCode(t, "set-project", bind.code, 0, bind)

	rep, _ = e.healthCompact()
	api = rep.service(t, "demo", "api")
	assertEq(t, "bound status", api.Status, "running")
	assertEq(t, "bound health", api.Health, "healthy")
	assertEq(t, "bound container", api.Container, "blue-api-1")
	assertEq(t, "bound identity_note", api.IdentityNote, "")
	assertCode(t, "bound --fail-on-unhealthy", e.run("health", "--fail-on-unhealthy").code, 0, result{})

	// Unverifiable project (running project carries no working_dir/config_files).
	e2 := newEnv(t)
	e2.enrollStack("demo", "services:\n  api:\n    image: app:1\n")
	e2.docker.running(
		container{Project: "mystery", Service: "api", Name: "mystery-api-1", State: "running", Health: "healthy"},
	)
	rep2, _ := e2.healthCompact()
	api2 := rep2.service(t, "demo", "api")
	assertEq(t, "unverifiable status", api2.Status, "unknown")
	assertEq(t, "unverifiable health", api2.Health, "unknown")
	assertContains(t, "unverifiable note", api2.IdentityNote, "mystery", "set-project")
	assertCode(t, "unverifiable --fail-on-unhealthy", e2.run("health", "--fail-on-unhealthy").code, 2, result{})
}

// Every Replica of a Service is inspected; the reported row carries the worst
// Replica (with lexical tie-break on container name) and deterministic counts
// of disagreeing Replicas regardless of Docker's response order.
func TestHealthInspectsAllReplicasAndReportsWorstDeterministically(t *testing.T) {
	e := newEnv(t)
	e.enrollStack("demo", "services:\n  api:\n    image: app:1\n  tied:\n    image: app:1\n")

	r1 := container{Project: "demo", Service: "api", Name: "demo-api-1", State: "running", Health: "healthy"}
	r2 := container{Project: "demo", Service: "api", Name: "demo-api-2", State: "running", Health: "unhealthy"}
	r3 := container{Project: "demo", Service: "api", Name: "demo-api-3", State: "running", Health: "healthy"}
	tB := container{Project: "demo", Service: "tied", Name: "demo-tied-b", State: "running", Health: "unhealthy"}
	tA := container{Project: "demo", Service: "tied", Name: "demo-tied-a", State: "running", Health: "unhealthy"}

	e.docker.running(r1, r2, r3, tB, tA)
	first, firstRes := e.healthCompact()

	e.docker.running(r3, r2, r1, tA, tB)
	_, secondRes := e.healthCompact()

	assertEq(t, "order independence", firstRes.stdout, secondRes.stdout)

	api := first.service(t, "demo", "api")
	assertEq(t, "worst container", api.Container, "demo-api-2")
	assertEq(t, "worst status", api.Status, "running")
	assertEq(t, "worst health", api.Health, "unhealthy")
	assertEq(t, "replicas", api.Replicas, 3)
	assertEq(t, "disagreeing", api.DisagreeingReplicas, 2)
	assertContains(t, "replica_detail", api.ReplicaDetail, "of 3 replicas", "1 running/unhealthy", "2 running/healthy")

	tied := first.service(t, "demo", "tied")
	assertEq(t, "lexical tie-break container", tied.Container, "demo-tied-a")
	assertEq(t, "tied replicas", tied.Replicas, 2)
	assertEq(t, "tied disagreeing", tied.DisagreeingReplicas, 0)
	assertEq(t, "tied replica_detail", tied.ReplicaDetail, "")

	table := e.run("health")
	assertContains(t, "table replica detail", table.stdout, "demo-api-2", "of 3 replicas")
	assertCode(t, "disagreeing --fail-on-unhealthy", e.run("health", "--fail-on-unhealthy").code, 2, result{})

	// Exited beats running/unhealthy.
	e.docker.running(
		container{Project: "demo", Service: "api", Name: "demo-api-1", State: "running", Health: "unhealthy"},
		container{Project: "demo", Service: "api", Name: "demo-api-2", State: "exited", Health: ""},
		container{Project: "demo", Service: "tied", Name: "demo-tied-a", State: "running", Health: "healthy"},
	)
	rep, _ := e.healthCompact()
	api = rep.service(t, "demo", "api")
	assertEq(t, "exited beats unhealthy container", api.Container, "demo-api-2")
	assertEq(t, "exited beats unhealthy status", api.Status, "exited")
	assertEq(t, "exited beats unhealthy health", api.Health, "none")
	assertEq(t, "exited beats unhealthy disagreeing", api.DisagreeingReplicas, 1)
}

// Unparseable and missing Stacks stay visible as structured stack_failures
// alongside usable Stacks, never silently vanish, never invent fake per-Service
// rows, and fail --fail-on-unhealthy with exit 2.
func TestHealthReportsUnparseableAndMissingStacksAsStructuredFailures(t *testing.T) {
	e := newEnv(t)
	e.enrollStack("demo", "services:\n  api:\n    image: app:1\n")
	e.enrollStack("broken", "services: [\n")
	e.enrollStack("gone", "services:\n  web:\n    image: app:1\n")
	if err := os.RemoveAll(filepath.Join(e.stacks(), "gone")); err != nil {
		t.Fatal(err)
	}
	e.docker.running(
		container{Project: "demo", Service: "api", Name: "demo-api-1", State: "running", Health: "healthy"},
	)

	rep, res := e.healthCompact()
	assertCode(t, "health --compact with failures", res.code, 0, res)
	assertContains(t, "stderr warnings", res.stderr, "warning:", "broken", "gone")

	// Only demo's real service appears in services; no invented rows for broken/gone.
	assertEq(t, "services len", len(rep.Services), 1)
	assertEq(t, "demo api status", rep.service(t, "demo", "api").Status, "running")
	assertEq(t, "demo api health", rep.service(t, "demo", "api").Health, "healthy")

	// Both broken and gone appear in stack_failures, sorted deterministically.
	assertEq(t, "stack_failures len", len(rep.StackFailures), 2)
	assertEq(t, "first failure stack", rep.StackFailures[0].Stack, "broken")
	if rep.StackFailures[0].Reason == "" {
		t.Error("broken stack failure has empty reason")
	}
	assertEq(t, "second failure stack", rep.StackFailures[1].Stack, "gone")
	assertContains(t, "gone reason", rep.StackFailures[1].Reason, "no longer exists")

	// Even though demo/api is healthy, incomplete stack failures cannot exit 0
	// under --fail-on-unhealthy.
	gate := e.run("health", "--fail-on-unhealthy")
	assertCode(t, "incomplete stack failures --fail-on-unhealthy", gate.code, 2, gate)

	// When every enrolled stack fails to parse, services is [] and
	// stack_failures still reports the failure.
	e2 := newEnv(t)
	e2.enrollStack("onlybroken", "services: [\n")
	rep2, res2 := e2.healthCompact()
	assertCode(t, "all broken default exit", res2.code, 0, res2)
	assertEq(t, "all broken services len", len(rep2.Services), 0)
	assertEq(t, "all broken stack_failures len", len(rep2.StackFailures), 1)
	assertEq(t, "all broken stack name", rep2.StackFailures[0].Stack, "onlybroken")
	assertCode(t, "all broken --fail-on-unhealthy", e2.run("health", "--fail-on-unhealthy").code, 2, result{})
}
