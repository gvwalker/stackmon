package report

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gvwalker/stackmon/internal/compose"
	"github.com/gvwalker/stackmon/internal/config"
	"github.com/gvwalker/stackmon/internal/imageref"
	"github.com/gvwalker/stackmon/internal/local"
	"github.com/gvwalker/stackmon/internal/registry"
)

// fakeRegistry records how often each reference was inspected, which is how
// deduplication is verified.
type fakeRegistry struct {
	mu       sync.Mutex
	images   map[string]registry.Image
	tags     map[string][]string
	inspects map[string]int
	tagCalls map[string]int
	err      error
	// tagsErr, when set, is returned only by Tags, so a test can simulate a
	// registry that serves Inspect fine but fails to list tags.
	tagsErr error
}

func (f *fakeRegistry) Inspect(_ context.Context, ref string) (registry.Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inspects == nil {
		f.inspects = map[string]int{}
	}
	f.inspects[ref]++
	if f.err != nil {
		return registry.Image{}, f.err
	}
	img, ok := f.images[ref]
	if !ok {
		return registry.Image{}, errors.New("not found")
	}
	return img, nil
}

func (f *fakeRegistry) Tags(_ context.Context, repo string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tagCalls == nil {
		f.tagCalls = map[string]int{}
	}
	f.tagCalls[repo]++
	if f.tagsErr != nil {
		return nil, f.tagsErr
	}
	return f.tags[repo], nil
}

type fakeDockerProber struct {
	containers []local.Container
	available  bool
}

func (f fakeDockerProber) Containers(context.Context) ([]local.Container, error) {
	return f.containers, nil
}
func (f fakeDockerProber) Available() bool { return f.available }

func stack(t *testing.T, name, service, raw string) compose.Stack {
	t.Helper()
	ref, err := imageref.Parse(raw, raw)
	if err != nil {
		t.Fatal(err)
	}
	return compose.Stack{
		Name:     name,
		Services: []compose.Service{{Name: service, Ref: ref}},
	}
}

func TestBuildReportsUpdateAvailable(t *testing.T) {
	reg := &fakeRegistry{
		images: map[string]registry.Image{
			"ghcr.io/vectorize-io/hindsight:0.9.1": {Digest: "sha256:a"},
		},
		tags: map[string][]string{
			"ghcr.io/vectorize-io/hindsight": {"0.9.1", "0.9.2", "1.0.0"},
		},
	}

	r := Build(context.Background(),
		[]compose.Stack{stack(t, "hindsight", "app", "ghcr.io/vectorize-io/hindsight:0.9.1")},
		Options{Registry: reg, Docker: fakeDockerProber{}, Concurrency: 2})

	if len(r.Images) != 1 {
		t.Fatalf("Images = %d, want 1", len(r.Images))
	}
	got := r.Images[0]
	if got.Status != StatusUpdateAvailable {
		t.Errorf("Status = %q, want update-available (err: %q)", got.Status, got.Err)
	}
	if got.Candidate != "1.0.0" {
		t.Errorf("Candidate = %q, want 1.0.0", got.Candidate)
	}
}

// A digest-only pin has no tag, so its version must come from the label.
func TestBuildRecoversVersionFromLabelForDigestOnlyPin(t *testing.T) {
	const ref = "traefik@sha256:9c3b91d5fb7770853ca5c1124a23c34bf2d9b47ffaebeab2614cbaf410dcb2ac"
	reg := &fakeRegistry{
		images: map[string]registry.Image{
			ref: {
				Digest: "sha256:9c3b91d5fb7770853ca5c1124a23c34bf2d9b47ffaebeab2614cbaf410dcb2ac",
				Labels: map[string]string{
					registry.LabelVersion: "v3.7.10",
					registry.LabelSource:  "https://github.com/traefik/traefik",
				},
			},
		},
		tags: map[string][]string{"index.docker.io/library/traefik": {"v3.7.10", "v3.8.0"}},
	}

	r := Build(context.Background(),
		[]compose.Stack{stack(t, "traefik", "traefik", ref)},
		Options{Registry: reg, Docker: fakeDockerProber{}, Concurrency: 1})

	got := r.Images[0]
	if got.Version != "v3.7.10" {
		t.Errorf("Version = %q, want v3.7.10 from the label", got.Version)
	}
	if got.Candidate != "v3.8.0" {
		t.Errorf("Candidate = %q, want v3.8.0", got.Candidate)
	}
	if got.NotesRepo != "traefik/traefik" {
		t.Errorf("NotesRepo = %q, want traefik/traefik", got.NotesRepo)
	}
}

func TestBuildDeduplicatesRepeatedReferences(t *testing.T) {
	reg := &fakeRegistry{
		images: map[string]registry.Image{"pgvector/pgvector:pg15": {Digest: "sha256:a"}},
	}

	Build(context.Background(), []compose.Stack{
		stack(t, "a", "db", "pgvector/pgvector:pg15"),
		stack(t, "b", "db", "pgvector/pgvector:pg15"),
	}, Options{Registry: reg, Docker: fakeDockerProber{}, Concurrency: 4})

	if n := reg.inspects["pgvector/pgvector:pg15"]; n != 1 {
		t.Errorf("inspected the same reference %d times, want 1", n)
	}
}

// An untrackable tag must not trigger a tag listing at all.
func TestBuildSkipsTagListingForOpaqueTags(t *testing.T) {
	reg := &fakeRegistry{
		images: map[string]registry.Image{"lscr.io/linuxserver/sonarr:latest": {Digest: "sha256:a"}},
		tags:   map[string][]string{"lscr.io/linuxserver/sonarr": {"latest", "1.0.0"}},
	}

	r := Build(context.Background(),
		[]compose.Stack{stack(t, "media", "sonarr", "lscr.io/linuxserver/sonarr:latest")},
		Options{Registry: reg, Docker: fakeDockerProber{}, Concurrency: 1})

	if got := r.Images[0].Candidate; got != "" {
		t.Errorf("Candidate = %q, want empty for an opaque tag", got)
	}
	if n := reg.tagCalls["lscr.io/linuxserver/sonarr"]; n != 0 {
		t.Errorf("Tags called %d times for an opaque tag, want 0", n)
	}
}

func TestBuildRegistryFailureBecomesUnknownRowNotFatal(t *testing.T) {
	reg := &fakeRegistry{err: errors.New("registry unreachable")}

	r := Build(context.Background(), []compose.Stack{
		stack(t, "a", "app", "redis:8.2"),
	}, Options{Registry: reg, Docker: fakeDockerProber{}, Concurrency: 1})

	if len(r.Images) != 1 {
		t.Fatalf("Images = %d, want 1: a failure must still produce a row", len(r.Images))
	}
	if r.Images[0].Status != StatusUnknown {
		t.Errorf("Status = %q, want unknown", r.Images[0].Status)
	}
	if r.Images[0].Err == "" {
		t.Error("Err is empty; the reason must be reported")
	}
}

func TestBuildMatchesRunningContainerByComposeLabels(t *testing.T) {
	reg := &fakeRegistry{
		images: map[string]registry.Image{"redis:8.2": {Digest: "sha256:new"}},
		tags:   map[string][]string{"index.docker.io/library/redis": {"8.2"}},
	}
	docker := fakeDockerProber{
		available: true,
		containers: []local.Container{
			{Project: "cache", Service: "redis", RepoDigest: "sha256:old"},
		},
	}

	r := Build(context.Background(),
		[]compose.Stack{stack(t, "cache", "redis", "redis:8.2")},
		Options{Registry: reg, Docker: docker, Concurrency: 1})

	got := r.Images[0]
	if got.RunningDigest != "sha256:old" {
		t.Errorf("RunningDigest = %q, want sha256:old", got.RunningDigest)
	}
	if got.Status != StatusStaleDeployment {
		t.Errorf("Status = %q, want stale-deployment", got.Status)
	}
}

func TestBuildOmitsRunningSignalWhenDockerUnavailable(t *testing.T) {
	reg := &fakeRegistry{
		images: map[string]registry.Image{"redis:8.2": {Digest: "sha256:a"}},
		tags:   map[string][]string{"index.docker.io/library/redis": {"8.2"}},
	}

	r := Build(context.Background(),
		[]compose.Stack{stack(t, "cache", "redis", "redis:8.2")},
		Options{Registry: reg, Docker: fakeDockerProber{available: false}, Concurrency: 1})

	if r.DockerAvailable {
		t.Error("DockerAvailable = true, want false")
	}
	if r.Images[0].Status == StatusNotRunning {
		t.Error("Status = not-running, but Docker was never checked")
	}
}

func TestBuildAppliesConfiguredConstraintOverride(t *testing.T) {
	reg := &fakeRegistry{
		images: map[string]registry.Image{"pgvector/pgvector:pg15": {Digest: "sha256:a"}},
		tags:   map[string][]string{"index.docker.io/pgvector/pgvector": {"pg15", "pg18", "pg18.1"}},
	}
	cfg := config.Config{Stacks: map[string]config.StackConfig{
		"db": {Images: map[string]config.ImageConfig{
			"pgvector/pgvector": {Constraint: "pg18*"},
		}},
	}}

	r := Build(context.Background(),
		[]compose.Stack{stack(t, "db", "postgres", "pgvector/pgvector:pg15")},
		Options{Registry: reg, Docker: fakeDockerProber{}, Config: cfg, Concurrency: 1})

	if got := r.Images[0].Candidate; got != "pg18.1" {
		t.Errorf("Candidate = %q, want pg18.1 from the configured constraint", got)
	}
}

func TestBuildSetsGeneratedTimestamp(t *testing.T) {
	reg := &fakeRegistry{images: map[string]registry.Image{"redis:8.2": {Digest: "sha256:a"}}}
	before := time.Now().Add(-time.Second)

	r := Build(context.Background(),
		[]compose.Stack{stack(t, "a", "redis", "redis:8.2")},
		Options{Registry: reg, Docker: fakeDockerProber{}, Concurrency: 1})

	if r.Generated.Before(before) {
		t.Errorf("Generated = %v, want a recent timestamp", r.Generated)
	}
}

// An image can be both behind a version and undeployed at once; Statuses
// must retain every applicable status even though Status reports only the
// most actionable one. A regression that made applicable() stop at the
// first match would pass every other test in this file while silently
// dropping this contract.
func TestBuildRetainsAllApplicableStatuses(t *testing.T) {
	const declared = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const running = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const ref = "ghcr.io/acme/app:1.0.0@" + declared
	reg := &fakeRegistry{
		images: map[string]registry.Image{ref: {Digest: declared}},
		tags:   map[string][]string{"ghcr.io/acme/app": {"1.0.0", "1.1.0"}},
	}
	docker := fakeDockerProber{
		available: true,
		containers: []local.Container{
			{Project: "acme", Service: "app", RepoDigest: running},
		},
	}

	r := Build(context.Background(),
		[]compose.Stack{stack(t, "acme", "app", ref)},
		Options{Registry: reg, Docker: docker, Concurrency: 1})

	got := r.Images[0]
	if got.Status != StatusUpdateAvailable {
		t.Fatalf("Status = %q, want update-available", got.Status)
	}
	hasUpdate, hasNotDeployed := false, false
	for _, s := range got.Statuses {
		hasUpdate = hasUpdate || s == StatusUpdateAvailable
		hasNotDeployed = hasNotDeployed || s == StatusNotDeployed
	}
	if !hasUpdate || !hasNotDeployed {
		t.Errorf("Statuses = %v, want both update-available and not-deployed retained", got.Statuses)
	}
}

// CandidateDigest must be resolved from the candidate version specifically,
// never reused from RegistryDigest: the registry digest for the declared
// reference and for a different candidate version are two different
// lookups against two different tags.
func TestBuildResolvesCandidateDigest(t *testing.T) {
	const declaredDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const candidateDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	const ref = "ghcr.io/acme/app:1.0.0"
	reg := &fakeRegistry{
		images: map[string]registry.Image{
			ref:                      {Digest: declaredDigest},
			"ghcr.io/acme/app:1.1.0": {Digest: candidateDigest},
		},
		tags: map[string][]string{"ghcr.io/acme/app": {"1.0.0", "1.1.0"}},
	}

	r := Build(context.Background(),
		[]compose.Stack{stack(t, "acme", "app", ref)},
		Options{Registry: reg, Docker: fakeDockerProber{}, Concurrency: 2})

	got := r.Images[0]
	if got.Candidate != "1.1.0" {
		t.Fatalf("Candidate = %q, want 1.1.0", got.Candidate)
	}
	if got.CandidateDigest != candidateDigest {
		t.Errorf("CandidateDigest = %q, want %q", got.CandidateDigest, candidateDigest)
	}
	if got.RegistryDigest == got.CandidateDigest {
		t.Errorf("RegistryDigest and CandidateDigest are both %q; the declared and candidate digests must be resolved independently", got.RegistryDigest)
	}
}

// A row whose candidate's digest can't be resolved must still report its
// status normally with CandidateDigest left empty: a registry hiccup on the
// second pass must not fail the run or corrupt Status, which is already
// correct from the first pass. Only the bump path loses information.
func TestBuildLeavesCandidateDigestEmptyWhenLookupFails(t *testing.T) {
	const ref = "ghcr.io/acme/app:1.0.0"
	reg := &fakeRegistry{
		images: map[string]registry.Image{ref: {Digest: "sha256:aaaa"}},
		tags:   map[string][]string{"ghcr.io/acme/app": {"1.0.0", "1.1.0"}},
		// No entry for "ghcr.io/acme/app:1.1.0": Inspect returns "not found".
	}

	r := Build(context.Background(),
		[]compose.Stack{stack(t, "acme", "app", ref)},
		Options{Registry: reg, Docker: fakeDockerProber{}, Concurrency: 2})

	got := r.Images[0]
	if got.Candidate != "1.1.0" {
		t.Fatalf("Candidate = %q, want 1.1.0", got.Candidate)
	}
	if got.Status != StatusUpdateAvailable {
		t.Errorf("Status = %q, want update-available even though the candidate digest lookup failed", got.Status)
	}
	if got.CandidateDigest != "" {
		t.Errorf("CandidateDigest = %q, want empty when the lookup fails", got.CandidateDigest)
	}
}

// A tag+digest pin's single Inspect fetches the pinned digest, which can
// never differ from itself. Only a separate inspect of the bare tag can see
// that the tag has moved on, which is what real digest drift is.
func TestBuildDetectsDigestDriftWhenTagHasMoved(t *testing.T) {
	const declaredDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const movedDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const pinnedRef = "ghcr.io/acme/app:1.0.0@" + declaredDigest
	oldCreated := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newCreated := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	reg := &fakeRegistry{
		images: map[string]registry.Image{
			pinnedRef: {
				Digest:  declaredDigest,
				Created: oldCreated,
				Labels:  map[string]string{registry.LabelRevision: "rev-old"},
			},
			"ghcr.io/acme/app:1.0.0": {
				Digest:  movedDigest,
				Created: newCreated,
				Labels:  map[string]string{registry.LabelRevision: "rev-new"},
			},
		},
		tags: map[string][]string{"ghcr.io/acme/app": {"1.0.0"}},
	}

	r := Build(context.Background(),
		[]compose.Stack{stack(t, "acme", "app", pinnedRef)},
		Options{Registry: reg, Docker: fakeDockerProber{}, Concurrency: 1})

	got := r.Images[0]
	if got.Status != StatusDigestDrift {
		t.Fatalf("Status = %q, want digest-drift (err: %q)", got.Status, got.Err)
	}
	if got.RegistryDigest != movedDigest {
		t.Errorf("RegistryDigest = %q, want the tag's current digest %q, not the declared pin", got.RegistryDigest, movedDigest)
	}
	if got.Created.Equal(got.RegistryCreated) {
		t.Errorf("Created and RegistryCreated are both %v; they must describe the declared and current images independently", got.Created)
	}
	if got.Revision == got.RegistryRevision {
		t.Errorf("Revision and RegistryRevision are both %q; they must describe the declared and current images independently", got.Revision)
	}
}

// A failed tag listing must degrade the row to unknown, not fall through to
// a silent "current": the constraint never actually ran.
func TestBuildTagListingFailureBecomesUnknownRow(t *testing.T) {
	reg := &fakeRegistry{
		images:  map[string]registry.Image{"redis:8.2": {Digest: "sha256:a"}},
		tagsErr: errors.New("registry unreachable"),
	}

	r := Build(context.Background(), []compose.Stack{
		stack(t, "a", "app", "redis:8.2"),
	}, Options{Registry: reg, Docker: fakeDockerProber{}, Concurrency: 1})

	got := r.Images[0]
	if got.Status != StatusUnknown {
		t.Errorf("Status = %q, want unknown when tag listing failed", got.Status)
	}
	if got.Err == "" {
		t.Error("Err is empty; a failed tag listing must be reported, not silently reported as current")
	}
}

// A configured constraint that matches nothing never actually ran the check
// the user asked for; reporting "current" would be a wrong answer presented
// as authoritative.
func TestBuildConstraintMatchingNoTagsBecomesUnknownRow(t *testing.T) {
	reg := &fakeRegistry{
		images: map[string]registry.Image{"postgres:18-alpine": {Digest: "sha256:a"}},
		tags:   map[string][]string{"index.docker.io/library/postgres": {"pg15", "pg16"}},
	}
	cfg := config.Config{Stacks: map[string]config.StackConfig{
		"db": {Images: map[string]config.ImageConfig{
			"library/postgres": {Constraint: "postgres18*"},
		}},
	}}

	r := Build(context.Background(),
		[]compose.Stack{stack(t, "db", "postgres", "postgres:18-alpine")},
		Options{Registry: reg, Docker: fakeDockerProber{}, Config: cfg, Concurrency: 1})

	got := r.Images[0]
	if got.Status != StatusUnknown {
		t.Errorf("Status = %q, want unknown when the constraint matches no tags", got.Status)
	}
	if got.Err == "" {
		t.Error("Err is empty; a constraint matching nothing must be reported, not silently reported as current")
	}
}
