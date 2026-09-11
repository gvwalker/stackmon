# Container Discovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Discover and explicitly enroll Compose stacks using labels on currently running Docker Compose containers, in addition to configured filesystem roots.

**Architecture:** Extend `local.Container` with the Compose project directory and configuration-file labels already returned by Docker. Keep root walking unchanged; `discover` constructs Docker candidates and merges candidates by cleaned directory. CLI commands receive a Docker prober through package-level constructors so tests can use a fake and production still constructs `local.New()`.

**Tech Stack:** Go 1.27, standard library, Cobra, existing direct Docker Engine API client.

**Spec:** `docs/superpowers/specs/2026-09-11-container-discovery-design.md`

## Global Constraints

- Docker discovery is default behavior; Docker unavailability degrades `discover` to root-only results.
- Discovery never changes Docker state or enrolls anything implicitly.
- No test may access a real Docker socket; fake `local.Prober` implementations provide containers.
- Configured-root depth, hidden-directory, filename precedence, and missing-root behavior remain unchanged.
- Existing `enroll <path>` behavior remains unchanged; `--running <project>` is mutually exclusive with a path argument.
- Run `gofmt -w` only on changed Go files; run `go test ./...`, `go vet ./...`, and `go build ./cmd/stackmon` once in final verification.

---

### Task 1: Expose Compose project location metadata

**Files:**
- Modify: `internal/local/local.go:31-159`
- Modify: `internal/local/local_test.go:69-103`

**Interfaces:**
- Produces: `local.Container{Project, ProjectWorkingDir, ProjectConfigFiles, Service, Image, RepoDigests}`.
- Consumes: Docker's existing `Labels` object from `/v1.44/containers/json`.

- [ ] **Step 1: Write the failing test**

Extend `TestContainersReadsComposeLabels` with both project-location labels in its Docker JSON fixture and assert that `got[0].ProjectWorkingDir` and `got[0].ProjectConfigFiles` equal those values:

```go
if got[0].ProjectWorkingDir != "/srv/compose/media" {
    t.Errorf("ProjectWorkingDir = %q, want /srv/compose/media", got[0].ProjectWorkingDir)
}
if got[0].ProjectConfigFiles != "/srv/compose/media/compose.yaml" {
    t.Errorf("ProjectConfigFiles = %q, want /srv/compose/media/compose.yaml", got[0].ProjectConfigFiles)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/local -run TestContainersReadsComposeLabels -count=1`

Expected: FAIL because `local.Container` has no project-location fields.

- [ ] **Step 3: Write the minimal implementation**

Add `ProjectWorkingDir` and `ProjectConfigFiles` strings to `Container`. Define label constants for `com.docker.compose.project.working_dir` and `com.docker.compose.project.config_files`; populate fields while constructing each container from `r.Labels`.

- [ ] **Step 4: Run the focused test to verify it passes**

Run: `go test ./internal/local -run TestContainersReadsComposeLabels -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/local/local.go internal/local/local_test.go
git commit -m "feat: expose compose project locations"
```

### Task 2: Build and merge Docker-discovered candidates

**Files:**
- Modify: `internal/discover/discover.go:27-109`
- Modify: `internal/discover/discover_test.go:27-149`

**Interfaces:**
- Consumes: `func DockerCandidates(containers []local.Container, inv inventory.Inventory) []Candidate` and `func Merge(candidates ...[]Candidate) []Candidate`.
- Produces: a sorted, deduplicated candidate slice whose `Enrolled` state comes from `inventory.Inventory`.

- [ ] **Step 1: Write the failing tests**

Add a test creating a temporary directory with `compose.yaml`, passing two services from one `Project: "media"` and matching location labels, then asserting `DockerCandidates` emits one candidate with that directory, `compose.yaml`, and name `media`.

Add a test where `Scan` finds that same directory and `Merge(rootCandidates, dockerCandidates)` produces exactly one candidate. Add a table row for missing or relative working directories and a missing compose file, asserting those Docker groups produce no candidates.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/discover -run 'TestDockerCandidates|TestMerge' -count=1`

Expected: FAIL to build because Docker candidate and merge functions do not exist.

- [ ] **Step 3: Write the minimal implementation**

Create an enrollment lookup helper shared by `Scan`, `DockerCandidates`, and `Merge`. `DockerCandidates` groups containers by project, requires an absolute cleaned working directory, uses `FindComposeFile`, and creates one candidate per valid directory. `Merge` deduplicates cleaned directories, recalculates enrollment, and sorts through the existing name-then-directory ordering.

- [ ] **Step 4: Run the focused tests to verify they pass**

Run: `go test ./internal/discover -run 'TestDockerCandidates|TestMerge|TestScan' -count=1`

Expected: PASS, including unchanged root scanner tests.

- [ ] **Step 5: Commit**

```bash
git add internal/discover/discover.go internal/discover/discover_test.go
git commit -m "feat: discover compose stacks from containers"
```

### Task 3: Wire default Docker discovery and running-project enrollment

**Files:**
- Modify: `cmd/stackmon/discover.go:12-46`
- Modify: `cmd/stackmon/enroll.go:15-51`
- Modify: `cmd/stackmon/enroll_test.go:1-53`
- Create: `cmd/stackmon/discover_test.go`

**Interfaces:**
- Consumes: `local.Prober`, `discover.DockerCandidates`, and `discover.Merge`.
- Produces: `stackmon discover` default Docker candidate output and `stackmon enroll --running <project>` inventory entries.

- [ ] **Step 1: Write the failing command tests**

Introduce a `fakeProber` implementing `local.Prober`, plus constructors accepting a prober in the discover/enroll command code. Test a no-root `discover` command with a fake Docker candidate and assert the table contains its name and path. Test unavailable Docker with a configured root and assert the root candidate remains listed. Test `enroll --running media` writes an inventory entry using the fake candidate's directory and compose filename. Test that a positional path plus `--running` is rejected.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/stackmon -run 'TestDiscover|TestEnrollRunning' -count=1`

Expected: FAIL because command constructors and `--running` do not exist.

- [ ] **Step 3: Write the minimal implementation**

Make `newDiscoverCmd` and `newEnrollCmd` delegate to constructors accepting `local.Prober`; production wrappers pass `local.New()`. `discover` scans roots when configured, conditionally reads Docker when `Available`, merges candidates, and always renders the table. `enroll` adds `--running`, validates exclusive selection, loads Docker containers, finds one project candidate, then reuses the current inventory-save path.

- [ ] **Step 4: Run the focused tests to verify they pass**

Run: `go test ./cmd/stackmon -run 'TestDiscover|TestEnrollRunning|TestResolve' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/stackmon/discover.go cmd/stackmon/discover_test.go cmd/stackmon/enroll.go cmd/stackmon/enroll_test.go
git commit -m "feat: enroll running compose projects"
```

### Task 4: Update user-facing contract and verify

**Files:**
- Modify: `README.md:7-15,44-66,81-100,160-181,199-206`

**Interfaces:**
- Consumes: final CLI behavior from Tasks 1–3.
- Produces: accurate quick-start, `discover`, and `enroll --running` documentation.

- [ ] **Step 1: Update documentation**

Change discovery wording from roots-only to roots plus currently running Compose projects. Document that Docker is optional and Docker-only candidates work without roots. Add a `stackmon enroll --running <project>` example and state that it resolves the working directory from Compose container labels. Retain the explicit-enrollment safety guarantee.

- [ ] **Step 2: Format changed Go files**

Run: `gofmt -w internal/local/local.go internal/local/local_test.go internal/discover/discover.go internal/discover/discover_test.go cmd/stackmon/discover.go cmd/stackmon/discover_test.go cmd/stackmon/enroll.go cmd/stackmon/enroll_test.go`

- [ ] **Step 3: Run final verification**

Run: `go test ./... && go vet ./... && go build ./cmd/stackmon`

Expected: all commands exit 0.

- [ ] **Step 4: Commit**

```bash
git add README.md docs/superpowers/specs/2026-09-11-container-discovery-design.md docs/superpowers/plans/2026-09-11-container-discovery.md
git commit -m "docs: explain container discovery"
```
