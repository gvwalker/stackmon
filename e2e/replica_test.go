package e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// A service is not one container. Two replicas at two digests is a half-rolled
// deployment, and which one Docker happens to list first is not a fact about
// the deployment: the verdict and the counts must be the same either way.
func TestMixedReplicasReportTheSameVerdictInEitherOrder(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{})
	old := e.push("app", "0.9.0", image{})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared))

	current := container{Project: "demo", Service: "api", ImageID: "sha256:new", RepoDigests: []string{"app@" + declared}}
	stale := container{Project: "demo", Service: "api", ImageID: "sha256:old", RepoDigests: []string{"app@" + old}}

	e.docker.running(current, stale)
	first := e.checkJSON().image(t, "demo", "api")
	e.docker.running(stale, current)
	second := e.checkJSON().image(t, "demo", "api")

	for _, r := range []row{first, second} {
		assertEq(t, "status", r.Status, "not-deployed")
		assertEq(t, "matching", r.ReplicaCount.Matching, 1)
		assertEq(t, "mismatching", r.ReplicaCount.Mismatching, 1)
		assertEq(t, "unknown", r.ReplicaCount.Unknown, 0)
		assertEq(t, "incomplete", r.ReplicaCount.Incomplete, false)
		assertEq(t, "compared against", r.ReplicaCount.Compared, declared)
		assertEq(t, "replica count", len(r.Replicas), 2)
	}
	assertEq(t, "replica order", replicaImages(first), replicaImages(second))
	assertContains(t, "table", e.run("check").stdout, "not-deployed", "replicas", "1 match sha256:", "1 differ from sha256:")
}

// Docker records a RepoDigests entry per manifest an image was ever pulled
// under. That makes a replica a match on any of them -- and says nothing at
// all about the replica beside it.
func TestAlternativeRepoDigestsMatchOneReplicaWithoutHidingAnother(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{})
	old := e.push("app", "0.9.0", image{})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared))
	e.docker.running(
		container{Project: "demo", Service: "api", ImageID: "sha256:retagged",
			RepoDigests: []string{"app@" + old, "app@" + declared}},
		container{Project: "demo", Service: "api", ImageID: "sha256:other", RepoDigests: []string{"app@" + old}},
	)

	row := e.checkJSON().image(t, "demo", "api")
	assertEq(t, "status", row.Status, "not-deployed")
	assertEq(t, "matching", row.ReplicaCount.Matching, 1)
	assertEq(t, "mismatching", row.ReplicaCount.Mismatching, 1)
}

// A replica with no recorded digest cannot be called current, and cannot be
// called wrong either. It is counted, and it keeps the verdict off current.
func TestAnUnjudgeableReplicaPreventsACurrentVerdict(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared))
	e.docker.running(
		container{Project: "demo", Service: "api", ImageID: "sha256:known", RepoDigests: []string{"app@" + declared}},
		container{Project: "demo", Service: "api", ImageID: "sha256:local", Image: "app:built-here"},
	)

	row := e.checkJSON().image(t, "demo", "api")
	if !row.Running {
		t.Error("Running = false, want true: a container is up, whatever its image records")
	}
	assertEq(t, "status", row.Status, "unknown")
	assertEq(t, "matching", row.ReplicaCount.Matching, 1)
	assertEq(t, "unknown", row.ReplicaCount.Unknown, 1)
	assertEq(t, "mismatching", row.ReplicaCount.Mismatching, 0)
	assertEq(t, "incomplete", row.ReplicaCount.Incomplete, true)

	// A detail view that hides the unknown replica behind "unknown" would
	// leave the user with nothing to act on.
	detail := e.run("show", "demo")
	assertCode(t, "show", detail.code, 0, detail)
	assertContains(t, "detail", detail.stdout, "replicas (2)", "match", "unknown, no recorded digest", "app:built-here", declared)
}

// A known mismatch is the actionable finding and stays the headline; the
// unjudgeable replica beside it is reported rather than swallowed.
func TestAMismatchSurvivesAnUnjudgeableReplica(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{})
	old := e.push("app", "0.9.0", image{})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared))
	e.docker.running(
		container{Project: "demo", Service: "api", ImageID: "sha256:known", RepoDigests: []string{"app@" + declared}},
		container{Project: "demo", Service: "api", ImageID: "sha256:old", RepoDigests: []string{"app@" + old}},
		container{Project: "demo", Service: "api", ImageID: "sha256:local", Image: "app:built-here"},
	)

	row := e.checkJSON().image(t, "demo", "api")
	assertEq(t, "status", row.Status, "not-deployed")
	assertEq(t, "mismatching", row.ReplicaCount.Mismatching, 1)
	assertEq(t, "unknown", row.ReplicaCount.Unknown, 1)
	assertEq(t, "incomplete", row.ReplicaCount.Incomplete, true)
	assertContains(t, "table", e.run("check").stdout, "not-deployed", "1 unknown")
}

// A floating tag has no pin, so a replica behind the registry is a stale
// deployment -- and the count of agreeing replicas does not make it whole.
func TestMixedReplicasOfAFloatingTagAreStale(t *testing.T) {
	e := newEnv(t)
	serving := e.push("app", "latest", image{})
	pulled := e.push("app", "1.0.0", image{})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "latest")))
	e.docker.running(
		container{Project: "demo", Service: "api", ImageID: "sha256:new", RepoDigests: []string{"app@" + serving}},
		container{Project: "demo", Service: "api", ImageID: "sha256:old", RepoDigests: []string{"app@" + pulled}},
	)

	row := e.checkJSON().image(t, "demo", "api")
	assertEq(t, "status", row.Status, "stale-deployment")
	assertEq(t, "compared against", row.ReplicaCount.Compared, serving)
	assertEq(t, "mismatching", row.ReplicaCount.Mismatching, 1)
}

// Replica uncertainty is a fact about the deployment, not about the registry,
// so it must not swallow the findings a user can act on today.
func TestKnownFindingsSurviveReplicaUncertainty(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{Revision: "rev-aaaa"})
	moved := e.push("app", "1.0.0", image{Revision: "rev-bbbb"})
	next := e.push("app", "1.1.0", image{})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared))
	// The running replica has no digest, so the deployment cannot be
	// compared, while the registry has both moved and moved on.
	e.docker.running(container{Project: "demo", Service: "api", ImageID: "sha256:local", Image: "app:built-here"})

	row := e.checkJSON().image(t, "demo", "api")
	assertEq(t, "status", row.Status, "update-available")
	assertEq(t, "candidate", row.Candidate, "1.1.0")
	assertEq(t, "candidate digest", row.CandidateDigest, next)
	assertEq(t, "registry digest", row.RegistryDigest, moved)
	assertContains(t, "all statuses", strings.Join(row.Statuses, ","), "update-available", "digest-drift", "unknown")

	// Drift alone is the headline when there is nothing newer to move to.
	e2 := newEnv(t)
	declared2 := e2.push("app", "1.0.0", image{Revision: "rev-aaaa"})
	e2.push("app", "1.0.0", image{Revision: "rev-bbbb"})
	e2.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e2.ref("app", "1.0.0"), declared2))
	e2.docker.running(container{Project: "demo", Service: "api", ImageID: "sha256:local", Image: "app:built-here"})

	drift := e2.checkJSON().image(t, "demo", "api")
	assertEq(t, "drift headline", drift.Status, "digest-drift")
	assertEq(t, "drift revision", drift.RegistryRevision, "rev-bbbb")
	assertEq(t, "deployment unjudgeable", drift.ReplicaCount.Unknown, 1)
}

// A `docker compose run` container is a finished job, not a replica. Counting
// it would make a stopped service look up.
func TestOneOffContainersAreNotReplicas(t *testing.T) {
	e := newEnv(t)
	declared := e.push("app", "1.0.0", image{})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n  worker:\n    image: %s@%s\n",
		e.ref("app", "1.0.0"), declared, e.ref("app", "1.0.0"), declared))
	// A project whose only container is a finished `docker compose run`: it
	// is up in the sense that a container exists, and not otherwise.
	batch := e.stack("batch", "services: {}\n")
	e.docker.running(
		container{Project: "demo", Service: "api", ImageID: "sha256:local", RepoDigests: []string{"app@" + declared}},
		container{Project: "demo", Service: "worker", OneOff: true, ImageID: "sha256:oneoff", Image: "app:one-off",
			RepoDigests: []string{"app@" + declared}},
		container{Project: "batch", WorkingDir: filepath.Dir(batch), ConfigFiles: batch, Service: "job",
			OneOff: true, ImageID: "sha256:oneoff", Image: "app:one-off"},
	)

	rep := e.checkJSON()
	api := rep.image(t, "demo", "api")
	assertEq(t, "api running", api.Status, "current")
	assertEq(t, "api replicas", len(api.Replicas), 1)

	worker := rep.image(t, "demo", "worker")
	assertEq(t, "worker status", worker.Status, "not-running")
	assertEq(t, "worker replicas", len(worker.Replicas), 0)
	assertContains(t, "table", e.run("check").stdout, "worker", "no running container")

	// Nor is it a project anyone could enroll from the daemon.
	discover := e.run("discover")
	assertCode(t, "discover", discover.code, 0, discover)
	assertOmits(t, "discover", discover.stdout, "batch")
}

// replicaImages lists a row's replicas as "image@digest" strings, so a test can
// compare the whole list rather than the count.
func replicaImages(r row) string {
	var out []string
	for _, replica := range r.Replicas {
		out = append(out, replica.Image+"@"+strings.Join(replica.Digests, "+"))
	}
	return strings.Join(out, " ")
}
