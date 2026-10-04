package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A tag with a newer version is the headline case: the candidate, the size of
// the jump, the digest bump would write, and the exit code cron alerts on.
func TestCheckOffersNewerVersionsWithTheirKind(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	nextDigest := e.push("app", "1.2.0", image{})
	e.push("tool", "1.0.0", image{})
	e.push("tool", "2.0.0", image{})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n  ci:\n    image: %s\n",
		e.ref("app", "1.0.0"), e.ref("tool", "1.0.0")))

	rep := e.checkJSON()
	api := rep.image(t, "demo", "api")
	assertEq(t, "app candidate", api.Candidate, "1.2.0")
	assertEq(t, "app kind", api.KindName, "minor")
	assertEq(t, "app status", api.Status, "update-available")
	assertEq(t, "app candidate digest", api.CandidateDigest, nextDigest)
	assertEq(t, "ci candidate", rep.image(t, "demo", "ci").Candidate, "2.0.0")
	assertEq(t, "ci kind", rep.image(t, "demo", "ci").KindName, "major")

	got := e.run("check")
	assertContains(t, "table", got.stdout, "STACK", "api", "1.0.0", "update-available", "1.2.0 available (minor)", "2.0.0 available (major)")
	assertCode(t, "check", got.code, 0, got)

	alert := e.run("check", "--fail-on-update")
	assertCode(t, "check --fail-on-update", alert.code, 2, alert)
	assertOmits(t, "fail-on-update", alert.stderr, "error:")
}

func TestCheckReportsCurrentWhenTheDeclaredTagIsNewest(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	deployed := e.push("app", "1.1.0", image{})
	e.docker.running(container{Project: "demo", Service: "api", ImageID: "sha256:local",
		RepoDigests: []string{"app@" + deployed}})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.1.0")))

	rep := e.checkJSON()
	api := rep.image(t, "demo", "api")
	assertEq(t, "status", api.Status, "current")
	assertEq(t, "version", api.Version, "1.1.0")
	assertEq(t, "candidate", api.Candidate, "")

	got := e.run("check")
	assertContains(t, "table", got.stdout, "api", "1.1.0", "current")
	assertCode(t, "check --fail-on-update", e.run("check", "--fail-on-update").code, 0, got)
}

// pg15 -> pg18 is a major database migration. stackmon must refuse to advise
// it, and must not even ask the registry for that repository's tags.
func TestCheckRefusesToAdviseAnOpaqueTag(t *testing.T) {
	e := newEnv(t)
	declared := e.push("pg", "pg15", image{})
	e.push("pg", "pg16", image{})
	e.push("pg", "pg18", image{})
	e.docker.running(container{Project: "db", Service: "postgres", ImageID: "sha256:local",
		RepoDigests: []string{"pg@" + declared}})
	e.enrollStack("db", fmt.Sprintf("services:\n  postgres:\n    image: %s\n", e.ref("pg", "pg15")))

	rep := e.checkJSON()
	row := rep.image(t, "db", "postgres")
	assertEq(t, "candidate", row.Candidate, "")
	assertEq(t, "status", row.Status, "current")
	if n := e.hits.count("GET /v2/pg/tags/list"); n != 0 {
		t.Errorf("listed tags for an untrackable tag %d times, want 0", n)
	}
}

// A configured glob is how a user opts an opaque tag back into tracking.
func TestCheckHonoursAConfiguredConstraint(t *testing.T) {
	e := newEnv(t)
	declared := e.push("pg", "pg15", image{})
	e.push("pg", "pg18", image{})
	e.push("pg", "pg18.1", image{})
	e.docker.running(container{Project: "db", Service: "postgres", ImageID: "sha256:local",
		RepoDigests: []string{"pg@" + declared}})
	e.config("[stacks.db.images.pg]\nconstraint = \"pg18*\"\n")
	e.enrollStack("db", fmt.Sprintf("services:\n  postgres:\n    image: %s\n", e.ref("pg", "pg15")))

	row := e.checkJSON().image(t, "db", "postgres")
	assertEq(t, "candidate", row.Candidate, "pg18.1")
	assertEq(t, "status", row.Status, "update-available")
}

// A linuxserver-style tag carries a build number after a three-component
// core. Ordering must compare that number, not the string: lexicographically
// "999" sorts after "1000".
func TestCheckRanksBuildNumbersNumerically(t *testing.T) {
	e := newEnv(t)
	deployed := e.push("lsio", "4.0.19.999-ls400", image{})
	e.push("lsio", "4.0.19.1000-ls401", image{})
	e.docker.running(container{Project: "media", Service: "app", ImageID: "sha256:local",
		RepoDigests: []string{"lsio@" + deployed}})
	e.config("[stacks.media.images.lsio]\nconstraint = \"4.0.19.*\"\n")
	e.enrollStack("media", fmt.Sprintf("services:\n  app:\n    image: %s\n", e.ref("lsio", "4.0.19.999-ls400")))

	row := e.checkJSON().image(t, "media", "app")
	assertEq(t, "candidate", row.Candidate, "4.0.19.1000-ls401")
	if len(row.Ordered) == 0 || row.Ordered[0] != "4.0.19.1000-ls401" {
		t.Errorf("ordered = %v, want the newer build first", row.Ordered)
	}
}

// A tag+digest pin whose tag has since been rebuilt: the same declared
// version, a different image. Drift is not an update.
func TestCheckDetectsDigestDriftWhenTheTagMoves(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{Revision: "rev-aaaa"})
	moved := e.push("app", "1.0.0", image{Revision: "rev-bbbb"})
	e.docker.running(container{Project: "demo", Service: "api", Image: e.ref("app", "1.0.0"),
		ImageID: "sha256:local", RepoDigests: []string{"app@" + declared}})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared))

	row := e.checkJSON().image(t, "demo", "api")
	assertEq(t, "status", row.Status, "digest-drift")
	assertEq(t, "registry digest", row.RegistryDigest, moved)
	assertEq(t, "declared digest", row.DeclaredDigest, declared)
	assertEq(t, "revision", row.Revision, "rev-aaaa")
	assertEq(t, "registry revision", row.RegistryRevision, "rev-bbbb")

	// Drift alone must not trip the update alert; only --drift-too counts it.
	assertCode(t, "check --fail-on-update", e.run("check", "--fail-on-update").code, 0, result{})
	assertCode(t, "check --fail-on-update --drift-too", e.run("check", "--fail-on-update", "--drift-too").code, 2, result{})
}

func TestCheckReportsNotDeployedWhenTheRunningImageDiffers(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{})
	other := e.push("app", "0.9.0", image{})
	e.docker.running(container{Project: "demo", Service: "api", ImageID: "sha256:local",
		RepoDigests: []string{"app@" + other}})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared))

	row := e.checkJSON().image(t, "demo", "api")
	assertEq(t, "status", row.Status, "not-deployed")
	assertContains(t, "table", e.run("check").stdout, "not-deployed", "declared pin not yet deployed")
}

// Docker records a RepoDigests entry per manifest the image has been pulled
// under, so the same repository can appear twice after a retag. Comparing
// only the first entry calls a correctly deployed image "not deployed".
func TestCheckAcceptsAnyRecordedRepoDigestAsDeployed(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{})
	older := e.push("app", "0.9.0", image{})
	e.docker.running(container{Project: "demo", Service: "api", ImageID: "sha256:local",
		RepoDigests: []string{"app@" + older, "app@" + declared}})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared))

	assertEq(t, "status", e.checkJSON().image(t, "demo", "api").Status, "current")
}

// A floating tag has no pin, so a running container behind the tag's current
// digest is a deployment that has fallen behind, not a version update.
func TestCheckReportsStaleDeploymentForAFloatingTag(t *testing.T) {
	e := newEnv(t)
	serving := e.push("app", "latest", image{})
	pulled := e.push("app", "1.0.0", image{})
	e.docker.running(container{Project: "demo", Service: "api", ImageID: "sha256:local",
		RepoDigests: []string{"app@" + pulled}})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "latest")))

	row := e.checkJSON().image(t, "demo", "api")
	assertEq(t, "status", row.Status, "stale-deployment")
	assertEq(t, "registry digest", row.RegistryDigest, serving)
	assertCode(t, "check --fail-on-update", e.run("check", "--fail-on-update").code, 0, result{})
}

func TestCheckReportsCurrentWhenARunningTagMatchesTheRegistry(t *testing.T) {
	e := newEnv(t)
	serving := e.push("app", "latest", image{})
	e.docker.running(container{Project: "demo", Service: "api", ImageID: "sha256:local",
		RepoDigests: []string{"app@" + serving}})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "latest")))

	assertEq(t, "status", e.checkJSON().image(t, "demo", "api").Status, "current")
}

func TestCheckReportsNotRunningWhenNoContainerExists(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0")))
	// A project running from a different directory says so in its labels, so
	// this stack is confirmed absent rather than merely unidentifiable. A
	// container stackmon did not start cannot be matched to a service at all,
	// so it must not turn up as somebody's running image either.
	other := e.stack("other", "services: {}\n")
	e.docker.running(
		container{Project: "other", Service: "api", WorkingDir: filepath.Dir(other), ConfigFiles: other, ImageID: "sha256:local"},
		container{Image: "nginx:latest", ImageID: "sha256:unmanaged"},
	)

	row := e.checkJSON().image(t, "demo", "api")
	assertEq(t, "status", row.Status, "not-running")
	assertContains(t, "table", e.run("check").stdout, "no running container")
}

// A container running a locally built image has no RepoDigests entry. Its
// presence is still a presence: "no digest" must not read as "not running".
// What it cannot support is a deployment verdict, so the row is unknown with
// the replica counted, not a container nobody has.
func TestCheckDoesNotCallAContainerWithoutDigestsNotRunning(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.docker.running(container{Project: "demo", Service: "api", Image: "app:local", ImageID: ""})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0")))

	row := e.checkJSON().image(t, "demo", "api")
	if !row.Running {
		t.Error("Running = false, want true: a container was found for the service")
	}
	if row.Status == "not-running" {
		t.Error("status = not-running, want a verdict about the image, not the container")
	}
	assertEq(t, "replicas", len(row.Replicas), 1)
	assertEq(t, "unjudgeable replicas", row.ReplicaCount.Unknown, 1)
}

func TestCheckReportsUnknownForAnImageTheRegistryDoesNotHave(t *testing.T) {
	e := newEnv(t)
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("ghost", "1.0.0")))

	got := e.run("check")
	row := e.checkJSON().image(t, "demo", "api")
	assertEq(t, "status", row.Status, "unknown")
	if row.Err == "" {
		t.Error("an unknown row must say why")
	}
	assertContains(t, "table", got.stdout, "unknown")
	assertCode(t, "check", got.code, 0, got)
}

// One unreachable registry must not take the whole report with it.
func TestCheckKeepsGoingWhenOneRegistryIsUnreachable(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n  legacy:\n    image: 127.0.0.1:1/ghost:1.0.0\n",
		e.ref("app", "1.0.0")))

	rep := e.checkJSON()
	assertEq(t, "reachable image", rep.image(t, "demo", "api").Status, "update-available")
	assertEq(t, "unreachable image", rep.image(t, "demo", "legacy").Status, "unknown")
	assertCode(t, "check", e.run("check").code, 0, result{})
}

// A constraint that matches nothing never ran the check the user asked for.
// Saying "current" would be a wrong answer dressed as an authoritative one.
func TestCheckReportsUnknownWhenAConstraintMatchesNoTags(t *testing.T) {
	e := newEnv(t)
	e.push("pg", "pg15", image{})
	e.config("[stacks.db.images.pg]\nconstraint = \"pg99*\"\n")
	e.enrollStack("db", fmt.Sprintf("services:\n  postgres:\n    image: %s\n", e.ref("pg", "pg15")))

	row := e.checkJSON().image(t, "db", "postgres")
	assertEq(t, "status", row.Status, "unknown")
	assertContains(t, "reason", row.Err, "no tags matched the constraint", "pg99*")
}

// Without the daemon the running-container signal is missing, not negative.
// Saying so is the difference between a partial answer and a wrong one.
func TestCheckSaysSoWhenDockerIsUnavailable(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.set("STACKMON_DOCKER_SOCKET", filepath.Join(e.root, "absent.sock"))
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0")))

	rep := e.checkJSON()
	assertEq(t, "docker_available", rep.DockerAvailable, false)
	if rep.image(t, "demo", "api").Status == "not-running" {
		t.Error("status = not-running, but the daemon was never consulted")
	}
	got := e.run("check")
	assertContains(t, "table", got.stdout, "docker socket unavailable")
	assertCode(t, "check", got.code, 0, got)
}

// A moved or unmounted stack is a fact about the world, not a stackmon
// failure: warn, keep going, exit 0. The same fact is carried in the
// report's Diagnostics, so a JSON consumer can tell a partial inventory
// from a healthy one.
func TestCheckReportsIncompleteWhenAStackMoved(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.enrollStack("gone", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0")))
	e.enrollStack("here", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0")))
	if err := os.RemoveAll(filepath.Join(e.stacks(), "gone")); err != nil {
		t.Fatal(err)
	}

	rep := e.checkJSON()
	d := rep.Diagnostics
	assertEq(t, "stacks checked", d.StacksChecked, 1)
	assertEq(t, "stacks skipped", d.StacksSkipped, 1)
	assertEq(t, "services checked", d.ServicesChecked, 1)
	if len(d.MissingStacks) != 1 || d.MissingStacks[0].Stack != "gone" {
		t.Errorf("missing stacks = %+v, want [gone]", d.MissingStacks)
	}
	assertCode(t, "check --fail-on-incomplete", e.run("check", "--fail-on-incomplete").code, 3, result{})
}

// A parse failure blanks no other stack, and the report must say so
// itself: skipped stack with the reason, alongside the stacks that did
// parse.
func TestCheckReportsAParseFailureInDiagnostics(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.enrollStack("broken", "services:\n  api:\n    image: ${REQUIRED_IMAGE:?must be set}\n")
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0")))

	rep := e.checkJSON()
	d := rep.Diagnostics
	assertEq(t, "stacks checked", d.StacksChecked, 1)
	assertEq(t, "stacks skipped", d.StacksSkipped, 1)
	if len(d.ParseFailures) != 1 || d.ParseFailures[0].Stack != "broken" || d.ParseFailures[0].Reason == "" {
		t.Errorf("parse failures = %+v, want broken with a reason", d.ParseFailures)
	}
	assertEq(t, "demo row kept", rep.image(t, "demo", "api").Status, "not-running")
	assertCode(t, "check --fail-on-incomplete", e.run("check", "--fail-on-incomplete").code, 3, result{})
}

// Every enrolled stack failing must not look like an empty inventory: the
// counts and the reasons say zero checked, N skipped.
func TestCheckDistinguishesAllStacksFailedFromEmpty(t *testing.T) {
	e := newEnv(t)
	e.enrollStack("broken", "services:\n  api:\n    image: ${REQUIRED_IMAGE:?must be set}\n")

	rep := e.checkJSON()
	d := rep.Diagnostics
	assertEq(t, "stacks checked", d.StacksChecked, 0)
	assertEq(t, "stacks skipped", d.StacksSkipped, 1)
	assertEq(t, "services checked", d.ServicesChecked, 0)
	assertEq(t, "images", len(rep.Images), 0)
	assertCode(t, "check --fail-on-incomplete", e.run("check", "--fail-on-incomplete").code, 3, result{})
}

// A socket that answers with an error keeps the reason, claims no verified
// running state, and marks the comparison incomplete. With no independently
// actionable finding the row cannot be "current" or "not-running".
func TestCheckWithAFailingDaemonDoesNotClaimVerifiedState(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.docker.fail()
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0")))

	rep := e.checkJSON()
	assertEq(t, "docker available", rep.DockerAvailable, false)
	if rep.DockerError == "" {
		t.Error("docker_error is empty, want the daemon's failure reason")
	}
	row := rep.image(t, "demo", "api")
	assertEq(t, "status", row.Status, "unknown")
	if row.RunningError == "" {
		t.Error("running_error is empty, want the daemon's failure reason")
	}
	got := e.run("check")
	assertContains(t, "table", got.stdout, "listing failed", "daemon is unhappy")
	assertCode(t, "check", got.code, 0, got)
	assertCode(t, "check --fail-on-incomplete", e.run("check", "--fail-on-incomplete").code, 3, result{})
}

// The failing-daemon case must not erase independently established
// findings: an update stays an update, now carrying the unverified-running
// caveat.
func TestCheckWithAFailingDaemonKeepsIndependentUpdatesVisible(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	e.docker.fail()
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0")))

	row := e.checkJSON().image(t, "demo", "api")
	assertEq(t, "status", row.Status, "update-available")
	if row.RunningError == "" {
		t.Error("running_error is empty on an update-available row, want the unverified-running caveat")
	}
	assertContains(t, "statuses", strings.Join(row.Statuses, ","), "update-available", "unknown")
	assertCode(t, "check --fail-on-incomplete", e.run("check", "--fail-on-incomplete").code, 3, result{})
}

// Absent optional socket keeps file/Registry-only behavior and never counts
// as a probe failure: default exit 0, and --fail-on-incomplete passes.
func TestCheckWithAbsentSocketIsNotAFailure(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.set("STACKMON_DOCKER_SOCKET", filepath.Join(e.root, "absent.sock"))
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0")))

	rep := e.checkJSON()
	assertEq(t, "docker available", rep.DockerAvailable, false)
	assertEq(t, "docker error", rep.DockerError, "")
	assertEq(t, "running_error", rep.image(t, "demo", "api").RunningError, "")
	assertContains(t, "table", e.run("check").stdout, "docker socket unavailable")
	assertCode(t, "check --fail-on-incomplete", e.run("check", "--fail-on-incomplete").code, 0, result{})
}

// A container-list failure hid running state from the report; an image that
// legitimately has no RepoDigests (built locally) is the opposite fact and
// must keep its distinct representation.
func TestCheckDistinguishesFailedInspectFromLegitimatelyAbsentDigests(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.docker.running(
		container{Project: "demo", Service: "api", ImageID: "sha256:local"},
		container{Project: "demo", Service: "local", Image: "app:local", ImageID: ""},
	)
	e.docker.failImageLookups()
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n  local:\n    image: %s\n",
		e.ref("app", "1.0.0"), e.ref("app", "1.0.0")))

	rep := e.checkJSON()
	api := rep.image(t, "demo", "api")
	if len(api.Replicas) != 1 || api.Replicas[0].DigestError == "" {
		t.Errorf("api replicas = %+v, want one with digest_error set", api.Replicas)
	}
	local := rep.image(t, "demo", "local")
	if len(local.Replicas) != 1 || local.Replicas[0].DigestError != "" || len(local.Replicas[0].Digests) != 0 {
		t.Errorf("local replicas = %+v, want one with neither digests nor digest_error", local.Replicas)
	}
	assertCode(t, "check --fail-on-incomplete", e.run("check", "--fail-on-incomplete").code, 3, result{})
}

// Exit 2 keeps its reserved meaning: when updates and incomplete checks
// coincide, --fail-on-update's code wins; incomplete alone is 3.
func TestCheckFailOnUpdateWinsOverIncompleteExits(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	e.docker.fail()
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0")))

	both := e.run("check", "--fail-on-update", "--fail-on-incomplete")
	assertCode(t, "updates + incomplete", both.code, 2, both)
	incompleteOnly := e.run("check", "--fail-on-incomplete")
	assertCode(t, "incomplete only", incompleteOnly.code, 3, incompleteOnly)
	e2 := newEnv(t)
	e2.push("app", "1.0.0", image{})
	e2.push("app", "1.1.0", image{})
	e2.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e2.ref("app", "1.0.0")))
	healthy := e2.run("check", "--fail-on-incomplete")
	assertCode(t, "healthy incomplete flag", healthy.code, 0, healthy)
}

// A moved or unmounted stack is a fact about the world, not a stackmon
// failure: warn, keep going, exit 0.
func TestCheckWarnsAboutAStackThatMoved(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.enrollStack("gone", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0")))
	e.enrollStack("here", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0")))
	if err := os.RemoveAll(filepath.Join(e.stacks(), "gone")); err != nil {
		t.Fatal(err)
	}

	got := e.run("check")
	assertCode(t, "check", got.code, 0, got)
	assertContains(t, "stderr", got.stderr, "warning:", "gone", "no longer exists")
	rep := e.checkJSON()
	if len(rep.Images) != 1 || rep.Images[0].Stack != "here" {
		t.Errorf("images = %+v, want only the stack that still exists", rep.Images)
	}
}

func TestCheckWarnsAboutAnUnparseableStack(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.enrollStack("broken", "services:\n  api:\n    image: ${REQUIRED_IMAGE:?must be set}\n")
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0")))

	got := e.run("check")
	assertCode(t, "check", got.code, 0, got)
	assertContains(t, "stderr", got.stderr, "warning:", "broken")
	rep := e.checkJSON()
	if len(rep.Images) != 1 || rep.Images[0].Stack != "demo" {
		t.Errorf("images = %+v, want only the stack that parsed", rep.Images)
	}
}

// One service whose image value stackmon cannot verify byte-for-byte must not
// erase its siblings.
func TestCheckKeepsSiblingsOfAnUnreadableService(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	body := fmt.Sprintf("services:\n  web:\n    image: |-\n      %s\n  api:\n    image: %s\n",
		e.ref("app", "1.0.0"), e.ref("app", "1.0.0"))
	e.enrollStack("demo", body)

	got := e.run("check")
	assertCode(t, "check", got.code, 0, got)
	assertContains(t, "stderr", got.stderr, "warning:", "web")
	rep := e.checkJSON()
	if len(rep.Images) != 1 || rep.Images[0].Service != "api" {
		t.Errorf("images = %+v, want only the readable sibling", rep.Images)
	}
}

// The real-world shapes of an image line: quoted, interpolated from .env, and
// a value preceded on its line by a multi-byte character (whose rune count,
// not byte count, is what YAML reports as the column).
func TestCheckReadsQuotedInterpolatedAndUnicodeImageValues(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	e.write(filepath.Join(e.stacks(), "demo", ".env"), "APP_VERSION=1.0.0\n", 0o644)
	body := fmt.Sprintf("services:\n"+
		"  quoted: {image: \"%s\"}\n"+
		"  env: {image: \"%s:${APP_VERSION}\"}\n"+
		"  café: {container_name: \"café-web\", image: %s}\n",
		e.ref("app", "1.0.0"), e.host+"/app", e.ref("app", "1.0.0"))
	e.enrollStack("demo", body)

	got := e.run("check")
	assertCode(t, "check", got.code, 0, got)
	assertOmits(t, "stderr", got.stderr, "warning:")

	rep := e.checkJSON()
	quoted := rep.image(t, "demo", "quoted")
	assertEq(t, "quoted ref", quoted.Ref.Resolved, e.ref("app", "1.0.0"))
	assertEq(t, "quoted raw", quoted.Ref.Raw, e.ref("app", "1.0.0"))
	assertEq(t, "quoted status", quoted.Status, "update-available")

	env := rep.image(t, "demo", "env")
	assertEq(t, "interpolated raw", env.Ref.Raw, e.host+"/app:${APP_VERSION}")
	assertEq(t, "interpolated resolved", env.Ref.Resolved, e.ref("app", "1.0.0"))
	assertEq(t, "interpolated vars", strings.Join(env.Ref.Vars, ","), "APP_VERSION")
	if !env.Ref.Interpolated {
		t.Error("Interpolated = false, want true")
	}

	// A value preceded by a multi-byte character on the same line is only
	// found when the recorded byte offset is computed by walking runes.
	uni := rep.image(t, "demo", "café")
	assertEq(t, "unicode ref", uni.Ref.Resolved, e.ref("app", "1.0.0"))
	assertEq(t, "unicode status", uni.Status, "update-available")
}

// The same reference in two stacks is one probe, not two: duplicated
// references are common and registry rate limits are not generous.
func TestCheckProbesASharedReferenceOnce(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	body := fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0"))
	e.enrollStack("a", body)
	e.enrollStack("b", body)

	e.hits.reset()
	rep := e.checkJSON()
	assertEq(t, "a", rep.image(t, "a", "api").Status, "update-available")
	assertEq(t, "b", rep.image(t, "b", "api").Status, "update-available")
	e.note("manifest fetches for app:1.0.0 across both stacks: %d", e.hits.count("GET /v2/app/manifests/1.0.0"))
	if n := e.hits.count("GET /v2/app/manifests/1.0.0"); n != 1 {
		t.Errorf("fetched the shared manifest %d times, want 1", n)
	}
}

// A digest-only pin has no tag to read, so the version comes from the
// org.opencontainers.image.version label, and updates are found from the
// repository's tags.
func TestCheckRecoversTheVersionOfADigestOnlyPin(t *testing.T) {
	e := newEnv(t)
	declared := e.push("proxy", "v3.7.10", image{Version: "v3.7.10"})
	e.push("proxy", "v3.8.0", image{Version: "v3.8.0"})
	next := e.push("proxy", "v3.9.0", image{Version: "v3.9.0"})
	e.docker.running(container{Project: "edge", Service: "proxy", ImageID: "sha256:local",
		RepoDigests: []string{"proxy@" + declared}})
	e.enrollStack("edge", fmt.Sprintf("services:\n  proxy:\n    image: %s\n", e.pin("proxy", declared)))

	row := e.checkJSON().image(t, "edge", "proxy")
	assertEq(t, "version", row.Version, "v3.7.10")
	assertEq(t, "candidate", row.Candidate, "v3.9.0")
	assertEq(t, "kind", row.KindName, "minor")
	assertEq(t, "candidate digest", row.CandidateDigest, next)
	assertEq(t, "status", row.Status, "update-available")
}

// Without a version label there is nothing to compare, so nothing is offered
// -- but the image is still perfectly healthy.
func TestCheckOffersNothingForAnUnlabelledDigestOnlyPin(t *testing.T) {
	e := newEnv(t)
	declared := e.push("proxy", "v3.7.10", image{})
	e.push("proxy", "v3.8.0", image{Version: "v3.8.0"})
	e.docker.running(container{Project: "edge", Service: "proxy", ImageID: "sha256:local",
		RepoDigests: []string{"proxy@" + declared}})
	e.enrollStack("edge", fmt.Sprintf("services:\n  proxy:\n    image: %s\n", e.pin("proxy", declared)))

	row := e.checkJSON().image(t, "edge", "proxy")
	assertEq(t, "version", row.Version, "")
	assertEq(t, "candidate", row.Candidate, "")
	assertEq(t, "status", row.Status, "current")
}

// Every status that applies is kept, even though the headline is only the
// most actionable one: an image can be both behind and undeployed.
func TestCheckRetainsEveryApplicableStatus(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	other := e.push("app", "0.9.0", image{})
	e.docker.running(container{Project: "demo", Service: "api", ImageID: "sha256:local",
		RepoDigests: []string{"app@" + other}})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared))

	row := e.checkJSON().image(t, "demo", "api")
	assertEq(t, "headline", row.Status, "update-available")
	assertContains(t, "all statuses", strings.Join(row.Statuses, ","), "update-available", "not-deployed")
}

// A multi-platform tag resolves to an index; the pin and RepoDigests hold the
// index digest. Comparing the platform-specific child instead makes every
// multi-arch image look permanently drifted.
func TestCheckComparesMultiArchImagesByTheirIndexDigest(t *testing.T) {
	e := newEnv(t)
	index := e.pushIndex("multi", "v1")
	e.push("multi", "v1.1.0", image{Version: "v1.1.0"})
	e.docker.running(container{Project: "demo", Service: "api", ImageID: "sha256:local",
		RepoDigests: []string{"multi@" + index}})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.pin("multi", index)))

	row := e.checkJSON().image(t, "demo", "api")
	assertEq(t, "registry digest", row.RegistryDigest, index)
	assertEq(t, "status", row.Status, "update-available")
}

// N containers sharing an image cost one lookup, not N.
func TestCheckLooksUpASharedRunningImageOnce(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.docker.running(
		container{Project: "demo", Service: "a", ImageID: "sha256:shared", RepoDigests: []string{"app@sha256:aa"}},
		container{Project: "demo", Service: "b", ImageID: "sha256:shared", RepoDigests: []string{"app@sha256:aa"}},
	)
	e.enrollStack("demo", fmt.Sprintf("services:\n  a:\n    image: %s\n  b:\n    image: %s\n",
		e.ref("app", "1.0.0"), e.ref("app", "1.0.0")))

	e.docker.reset()
	e.checkJSON()
	e.note("image lookups for 2 containers sharing one image: %d", e.docker.lookups())
	if n := e.docker.lookups(); n != 1 {
		t.Errorf("looked up the shared image %d times, want 1", n)
	}
}

// A broken config is a stackmon failure: exit 1, "error:" on stderr, no table.
func TestCheckRejectsABrokenConfig(t *testing.T) {
	for _, cfg := range []string{"roots = [", "concurrency = 0", "concurrency = -1", "[bump]\ndigest = \"yes\"\n"} {
		e := newEnv(t)
		e.push("app", "1.0.0", image{})
		e.config(cfg)
		e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0")))

		got := e.run("check")
		assertCode(t, fmt.Sprintf("check with config %q", cfg), got.code, 1, got)
		assertContains(t, "stderr", got.stderr, "error:")
	}
}

// A bare repository name means "the latest tag", and an opaque tag is never
// advanced. Both are the same rule seen from two ends.
func TestCheckReadsABareRepositoryNameAsLatest(t *testing.T) {
	e := newEnv(t)
	serving := e.push("app", "latest", image{})
	e.docker.running(container{Project: "demo", Service: "api", ImageID: "sha256:local",
		RepoDigests: []string{"app@" + serving}})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s/app\n", e.host))

	row := e.checkJSON().image(t, "demo", "api")
	assertEq(t, "resolved", row.Ref.Resolved, e.host+"/app")
	assertEq(t, "derived tag", row.Ref.Tag, "latest")
	assertEq(t, "version", row.Version, "latest")
	assertEq(t, "status", row.Status, "current")
}

// Tags compete within their own channel: an alpine tag must not be offered a
// bookworm one, or a bare one, and a v-prefixed tag must not be offered an
// unprefixed one.
func TestCheckKeepsCandidateTagsInOneChannel(t *testing.T) {
	e := newEnv(t)
	for _, tag := range []string{"18-alpine", "18-bookworm", "19", "19-bookworm", "19.1-alpine"} {
		e.push("app", tag, image{})
	}
	deployed := e.push("app", "18-alpine", image{})
	e.docker.running(container{Project: "demo", Service: "api", ImageID: "sha256:local",
		RepoDigests: []string{"app@" + deployed}})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "18-alpine")))

	row := e.checkJSON().image(t, "demo", "api")
	assertEq(t, "candidate", row.Candidate, "19.1-alpine")
	assertEq(t, "ordered", strings.Join(row.Ordered, ","), "19.1-alpine,18-alpine")
}

// A glob can match tags with no version in them at all. They are still
// candidates to show, ranked after everything with a version.
func TestCheckKeepsVersionlessGlobMatches(t *testing.T) {
	e := newEnv(t)
	for _, tag := range []string{"pg15", "pg18.1", "edge", "canary"} {
		e.push("pg", tag, image{})
	}
	deployed := e.push("pg", "pg15", image{})
	e.docker.running(container{Project: "db", Service: "postgres", ImageID: "sha256:local",
		RepoDigests: []string{"pg@" + deployed}})
	e.config("[stacks.db.images.pg]\nconstraint = \"*\"\n")
	e.enrollStack("db", fmt.Sprintf("services:\n  postgres:\n    image: %s\n", e.ref("pg", "pg15")))

	row := e.checkJSON().image(t, "db", "postgres")
	assertEq(t, "candidate", row.Candidate, "pg18.1")
	assertEq(t, "ordered", strings.Join(row.Ordered, ","), "pg18.1,pg15,edge,canary")
}

// A stack's name is the user's, not a compose project name: an uppercase
// directory must still load, exactly as Docker Compose handles it.
func TestCheckHandlesANonNormalisedStackName(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	e.write(filepath.Join(e.stacks(), "Nextcloud", "compose.yaml"),
		fmt.Sprintf("services:\n  cloud:\n    image: %s\n", e.ref("app", "1.0.0")), 0o644)
	e.config(fmt.Sprintf("roots = [%q]\n", e.stacks()))
	if r := e.enroll(filepath.Join(e.stacks(), "Nextcloud")); r.code != 0 {
		t.Fatalf("enrolling Nextcloud: %s%s", r.stdout, r.stderr)
	}

	got := e.run("check", "Nextcloud")
	assertCode(t, "check a capitalised stack", got.code, 0, got)
	assertContains(t, "check", got.stdout, "Nextcloud", "cloud", "1.1.0 available (minor)")
}

// The compact form exists to be read by a script: the verdict, what is running
// now, and the tag that would supersede it, with no field a reader acting on
// the verdict would be stuck without.
func TestCheckCompactCarriesTheVerdictAndWhatWouldChangeIt(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.2.0", image{})
	e.push("tool", "1.0.0", image{})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n  ci:\n    image: %s\n",
		e.ref("app", "1.0.0"), e.ref("tool", "1.0.0")))

	rep, _ := e.checkCompact()
	assertEq(t, "docker_available", rep.DockerAvailable, true)
	api := rep.image(t, "demo", "api")
	assertEq(t, "status", api.Status, "update-available")
	assertEq(t, "version", api.Version, "1.0.0")
	assertEq(t, "candidate", api.Candidate, "1.2.0")
	// A candidate turns update-available from a nag into an action; the
	// reason fields stay empty because the verdict is not in question.
	assertEq(t, "error", api.Err, "")
	assertEq(t, "identity note", api.IdentityNote, "")
	// The candidate belongs to the service, not to the stack.
	assertEq(t, "ci candidate", rep.image(t, "demo", "ci").Candidate, "")
}

// Exactly the promised fields, on a row that has the least to say: a field
// that appears only when it is populated is a field a consumer has to test
// for, and a field that was not promised is one it will come to depend on.
func TestCheckCompactCarriesOnlyTheFieldsItPromised(t *testing.T) {
	e := newEnv(t)
	deployed := e.push("app", "1.0.0", image{})
	e.docker.running(container{Project: "demo", Service: "api", ImageID: "sha256:local",
		RepoDigests: []string{"app@" + deployed}})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0")))

	// The row itself is the artifact: a compact document nobody can parse
	// without a schema is not compact.
	rep, doc := e.checkCompact()
	assertEq(t, "status", rep.image(t, "demo", "api").Status, "current")
	assertEqSlice(t, "top-level keys", compactKeys(t, doc), []string{"docker_available", "images"})
	assertEqSlice(t, "row keys", compactRowKeys(t, doc),
		[]string{"candidate", "error", "identity_note", "service", "stack", "status", "version"})
}

func TestCheckCompactSaysWhyARowIsUnknown(t *testing.T) {
	e := newEnv(t)
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("ghost", "1.0.0")))

	rep, _ := e.checkCompact()
	row := rep.image(t, "demo", "api")
	assertEq(t, "status", row.Status, "unknown")
	// Only one of the two reasons applies here, and the other stays empty
	// rather than saying something that did not happen.
	assertContains(t, "error", row.Err, "ghost")
	assertEq(t, "identity note", row.IdentityNote, "")
}

// An unresolved Compose project identity is the other route to unknown, and it
// carries its reason in identity_note with the error field empty.
func TestCheckCompactCarriesAnIdentityNoteWithoutAnError(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{})
	file := e.enrollStack("cache", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared))
	one := container{Project: "one", WorkingDir: filepath.Dir(file), ConfigFiles: file,
		Service: "api", ImageID: "sha256:one", RepoDigests: []string{"app@" + declared}}
	two := container{Project: "two", WorkingDir: filepath.Dir(file), ConfigFiles: file,
		Service: "api", ImageID: "sha256:two", RepoDigests: []string{"app@" + declared}}
	e.docker.running(one, two)

	rep, _ := e.checkCompact()
	row := rep.image(t, "cache", "api")
	assertEq(t, "status", row.Status, "unknown")
	assertEq(t, "error", row.Err, "")
	assertContains(t, "identity note", row.IdentityNote, "one", "two", "set-project")
}

// Every compact row is qualified by whether the daemon was reachable at all,
// so the flag that qualifies them cannot live inside one.
func TestCheckCompactSaysWhenDockerWasNeverConsulted(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.set("STACKMON_DOCKER_SOCKET", filepath.Join(e.root, "absent.sock"))
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0")))

	rep, _ := e.checkCompact()
	assertEq(t, "docker_available", rep.DockerAvailable, false)
	// A missing signal is not a negative one: without the daemon, "current"
	// would be a claim nothing checked.
	if s := rep.image(t, "demo", "api").Status; s == "not-running" {
		t.Errorf("status = %s, but the daemon was never consulted", s)
	}
}

// -c is the same document, and the exit code still carries the verdict: a
// script reading JSON and a script reading the exit code must not disagree.
func TestCheckCompactAcceptsTheShortForm(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "1.1.0", image{})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0")))

	short := e.exec(devBinary, "check", "-c", "--fail-on-update")
	full := e.exec(devBinary, "check", "--compact", "--fail-on-update")
	e.record([]string{"check", "-c", "--fail-on-update"}, short)
	assertEq(t, "-c and --compact", short.stdout, full.stdout)
	assertEq(t, "-c candidate", strings.Contains(short.stdout, `"candidate": "1.1.0"`), true)
	assertCode(t, "check -c --fail-on-update", short.code, 2, short)
}
