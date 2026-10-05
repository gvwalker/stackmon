package report

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/gvwalker/stackmon/internal/compose"
	"github.com/gvwalker/stackmon/internal/config"
	"github.com/gvwalker/stackmon/internal/imageref"
	"github.com/gvwalker/stackmon/internal/local"
	"github.com/gvwalker/stackmon/internal/notes"
	"github.com/gvwalker/stackmon/internal/policy"
	"github.com/gvwalker/stackmon/internal/registry"
)

// Options configures a run.
type Options struct {
	Registry *registry.Client
	Docker   *local.Client
	Config   config.Config
	// Concurrency bounds simultaneous registry probes; below 1 the default
	// is used.
	Concurrency int
}

// probe is what one unique reference at one platform resolves to. A
// reference is not one probe when two Services ask for it at different
// platforms: their metadata comes from different children of the same index,
// so a shared probe would answer both from the same one.
type probe struct {
	image registry.Image
	tags  []string
	err   error
	// platform is the effective target the probe ran at, empty only when the
	// declared platform could not be parsed and nothing was inspected.
	platform string
	// registryImage is a separate inspection of the bare tag (no digest)
	// for a ShapeTagDigest reference: what the tag currently resolves to
	// in the registry, as opposed to image, which describes the pinned
	// digest. Without it, RegistryDigest/RegistryCreated/RegistryRevision
	// would always equal the declared side by construction, so digest
	// drift and the built/revision explanation could never fire. Zero for
	// every other shape, where there is nothing further to check.
	registryImage    registry.Image
	hasRegistryImage bool
}

// target is one reference to inspect, at the platform Compose declares for it.
type target struct {
	ref      imageref.Ref
	platform string
}

// key is the probe cache's identity. The declared platform is part of it, not
// the resolved one: two Services that both end up on the default platform are
// the same probe, and one that declares a platform the other does not is not.
func (t target) key() string { return t.ref.Resolved + "|" + t.platform }

func targetFor(svc compose.Service) target {
	return target{ref: svc.Ref, platform: svc.Platform}
}

// DefaultPlatform names the platform a row is inspected at when its service
// declares none. A renderer needs it to stay quiet about the common case
// without carrying a second copy of the value, which would drift.
func DefaultPlatform() string { return registry.DefaultPlatform().String() }

// candidate is one candidate tag to resolve a digest for, at the platform its
// row was probed at.
type candidate struct {
	ref      string
	platform string
}

// candidateKey identifies a candidate lookup. The platform is part of what is
// being looked up, not a detail of the lookup: a candidate publishing only the
// declared platform is unreadable under the default one.
func candidateKey(ref imageref.Ref, candidate, platform string) string {
	return ref.Registry + "/" + ref.Repository + ":" + candidate + "|" + platform
}

// Build probes every image in every stack and assembles the report. It never
// returns an error: a per-image failure becomes an unknown row, so one
// unreachable registry cannot fail the run.
func Build(ctx context.Context, stacks []compose.Stack, opts Options) Report {
	if opts.Concurrency < 1 {
		opts.Concurrency = config.DefaultConcurrency
	}

	r := Report{Generated: time.Now().UTC()}

	// The running-container signal is optional. A socket that exists but
	// whose listing fails is not an absent signal: the failure reason is
	// threaded into the report so a verified "current" is never claimed from
	// a check that never ran.
	var containers []local.Container
	var runningErr string
	if opts.Docker != nil && opts.Docker.Available() {
		if cs, err := opts.Docker.Containers(ctx); err == nil {
			r.DockerAvailable = true
			containers = cs
		} else {
			runningErr = err.Error()
			r.DockerError = runningErr
		}
	}
	running := local.Index(containers)

	// Deduplicate: the same reference at the same platform is probed once.
	unique := map[string]target{}
	for _, st := range stacks {
		for _, svc := range st.Services {
			t := targetFor(svc)
			unique[t.key()] = t
		}
	}

	// Constraints depend on config, which is per stack, so collect the set of
	// constraints each target needs before probing.
	constraints := map[string]policy.Constraint{}
	for _, st := range stacks {
		for _, svc := range st.Services {
			t := targetFor(svc)
			c := constraintFor(opts.Config, st.Name, svc.Ref)
			// A trackable constraint anywhere means tags must be listed.
			if c.Trackable || !constraints[t.key()].Trackable {
				constraints[t.key()] = c
			}
		}
	}

	probes := make(map[string]probe, len(unique))
	var mu sync.Mutex

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(opts.Concurrency)

	for key, t := range unique {
		key, t := key, t
		g.Go(func() error {
			p := probe{}
			// Resolve the declared platform once and thread it through every
			// inspection below: a tag's current state and a candidate's digest
			// are as platform-specific as the declared reference's metadata is.
			platform, err := registry.Target(t.platform)
			if err != nil {
				p.err = err
			} else {
				p.image, p.err = opts.Registry.Inspect(gctx, t.ref.Resolved, platform)
			}
			p.platform = platform.String()

			// A tag+digest pin's single fetch above is pinned to the
			// declared digest, so it can never see what the tag currently
			// serves. Inspect the bare tag separately so RegistryDigest
			// describes the registry's current state, not the declared
			// one. Other shapes have no separate tag to check this way:
			// a tag-only ref's fetch above is already the current tag, and
			// a digest-only ref names no tag at all.
			if p.err == nil && t.ref.Shape == imageref.ShapeTagDigest {
				tagRef := t.ref.Registry + "/" + t.ref.Repository + ":" + t.ref.Tag
				img, err := opts.Registry.Inspect(gctx, tagRef, platform)
				if err != nil {
					// A failed tag-current inspect is a failed check, not a
					// silent "current": without it, RegistryDigest falls
					// back to the declared digest and digest-drift can
					// never fire.
					p.err = err
				} else {
					p.registryImage = img
					p.hasRegistryImage = true
				}
			}

			// Only list tags when a constraint could actually use them; a
			// floating tag such as :latest must not trigger the call. A
			// failure here must degrade the row to unknown, not fall
			// through to a silent "current": a constraint that needed
			// tags to evaluate never actually ran.
			if p.err == nil {
				needsTags := constraints[key].Trackable
				if needsTags {
					repo := t.ref.Registry + "/" + t.ref.Repository
					tags, err := opts.Registry.Tags(gctx, repo)
					if err != nil {
						p.err = err
					} else {
						p.tags = tags
					}
				}
			}

			mu.Lock()
			probes[key] = p
			mu.Unlock()
			return nil // Per-image failures are data, not run failures.
		})
	}
	_ = g.Wait() // No goroutine returns an error.

	for _, st := range stacks {
		// Which Compose project runs this stack is a property of the stack,
		// not of each service, so it is established once and the same answer
		// is given to every service in it.
		identity := resolveIdentity(st, containers, r.DockerAvailable)
		for _, svc := range st.Services {
			t := targetFor(svc)
			r.Images = append(r.Images, assemble(st, svc, probes[t.key()], running, identity, r.DockerAvailable, runningErr, opts.Config))
		}
	}

	// Second pass: resolve each unique candidate's digest, so bump can pair
	// an advancing tag with the correct new digest instead of the declared
	// reference's stale one. RegistryDigest above describes only the
	// declared reference and is never the candidate's digest. A failure
	// here must not fail the run or affect Status: only the bump path is
	// missing information. The digest written is the index digest whatever
	// platform it was resolved at: a pin rewritten to a child's manifest
	// would break every other platform the index publishes.
	candidateKeys := map[string]candidate{}
	for _, img := range r.Images {
		if img.Candidate == "" {
			continue
		}
		c := candidate{
			ref:      img.Ref.Registry + "/" + img.Ref.Repository + ":" + img.Candidate,
			platform: img.Platform,
		}
		candidateKeys[candidateKey(img.Ref, img.Candidate, img.Platform)] = c
	}

	if len(candidateKeys) > 0 {
		digests := make(map[string]string, len(candidateKeys))
		var dmu sync.Mutex

		cg, cgctx := errgroup.WithContext(ctx)
		cg.SetLimit(opts.Concurrency)
		for key, c := range candidateKeys {
			key, c := key, c
			cg.Go(func() error {
				// img.Platform is the canonical form of the platform the row
				// was probed at, so resolving it again is a round trip that
				// cannot fail in practice -- and a failure would cost nothing
				// beyond the empty CandidateDigest an unreachable registry
				// already leaves behind.
				platform, err := registry.Target(c.platform)
				if err != nil {
					return nil
				}
				if img, err := opts.Registry.Inspect(cgctx, c.ref, platform); err == nil {
					dmu.Lock()
					digests[key] = img.Digest
					dmu.Unlock()
				}
				return nil // A failed lookup just leaves CandidateDigest empty.
			})
		}
		_ = cg.Wait() // No goroutine returns an error.

		for i := range r.Images {
			if r.Images[i].Candidate == "" {
				continue
			}
			key := candidateKey(r.Images[i].Ref, r.Images[i].Candidate, r.Images[i].Platform)
			r.Images[i].CandidateDigest = digests[key]
		}
	}

	sort.Slice(r.Images, func(i, j int) bool {
		if r.Images[i].Stack != r.Images[j].Stack {
			return r.Images[i].Stack < r.Images[j].Stack
		}
		return r.Images[i].Service < r.Images[j].Service
	})
	return r
}

// constraintFor prefers an explicit config glob over inference.
func constraintFor(cfg config.Config, stack string, ref imageref.Ref) policy.Constraint {
	if glob := cfg.Image(stack, ref.Repository).Constraint; glob != "" {
		return policy.Parse(glob)
	}
	// A digest-only pin has no tag to infer from; its version arrives from
	// the config blob, so inference is deferred until assemble.
	if ref.Shape == imageref.ShapeDigestOnly {
		return policy.Constraint{Trackable: true}
	}
	return policy.Infer(ref.Tag)
}

// identity is the Compose project one stack's running state comes from, and
// why that is or is not established.
type identity struct {
	project string
	bound   bool
	note    string
}

// resolveIdentity decides which Docker Compose project a stack's services are
// running in. The enrolled display name is deliberately not consulted: Compose
// project names come from `name:`, `-p`, or the directory, so a name mismatch
// says nothing, and a name match proves nothing either.
//
// When the daemon was never consulted there is nothing to resolve and nothing
// to complain about: the running signal is simply absent.
func resolveIdentity(st compose.Stack, containers []local.Container, dockerUp bool) identity {
	if !dockerUp {
		return identity{}
	}
	id := local.ResolveProject(containers, st.Project, st.Dir, st.Path())
	if id.Resolved {
		return identity{project: id.Project, bound: id.Bound}
	}
	// No reason means the daemon reported every running container and none
	// of them is this stack's compose file: a fact, not a doubt.
	if id.Reason == "" {
		return identity{}
	}

	bind := fmt.Sprintf("stackmon inventory set-project %s <project>", st.Name)
	switch id.Reason {
	case local.ReasonAmbiguous:
		return identity{note: fmt.Sprintf(
			"ambiguous: %s all run %s; bind the one meant with %q",
			joinNames(id.Candidates), st.Path(), bind)}
	default:
		return identity{note: fmt.Sprintf(
			"cannot tell which Compose project runs %s: the running projects (%s) record no working directory or config file to match it against; bind it with %q",
			st.Path(), strings.Join(id.Candidates, ", "), bind)}
	}
}

// joinNames lists names the way a person would: "a", "a and b", "a, b and c".
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
}

// compareReplicas judges every Replica of a service against the digest it is
// declared at, or -- for a floating reference with no pin -- against the
// digest the registry currently serves. Each Replica is judged on its own
// digest set: Docker records an entry per manifest the image was ever pulled
// under, so any matching entry is a match, and that match says nothing about
// the Replicas beside it. A Replica with no digest to compare, or no digest to
// compare it against, is counted as unknown rather than folded into either
// verdict.
func compareReplicas(img *Image) {
	if len(img.Replicas) == 0 {
		return
	}
	compared := img.DeclaredDigest
	if compared == "" {
		compared = img.RegistryDigest
	}
	img.ReplicaComparison.Compared = compared

	for _, r := range img.Replicas {
		switch {
		case compared == "" || len(r.Digests) == 0:
			img.ReplicaComparison.Unknown++
		case containsDigest(r.Digests, compared):
			img.ReplicaComparison.Matching++
		default:
			img.ReplicaComparison.Mismatching++
		}
	}
	img.ReplicaComparison.Incomplete = img.ReplicaComparison.Unknown > 0
}

func assemble(st compose.Stack, svc compose.Service, p probe, running map[string][]local.Container, id identity, dockerUp bool, runningErr string, cfg config.Config) Image {
	img := Image{
		Stack:          st.Name,
		Service:        svc.Name,
		Ref:            svc.Ref,
		DeclaredDigest: svc.Ref.Digest,
		Platform:       p.platform,
		Project:        id.project,
		ProjectBound:   id.bound,
		IdentityNote:   id.note,
		DockerChecked:  dockerUp,
		RunningError:   runningErr,
	}

	for _, c := range running[id.project+"/"+svc.Name] {
		img.Replicas = append(img.Replicas, Replica{Image: c.Image, Digests: c.RepoDigests, DigestError: c.DigestError})
	}
	img.Running = len(img.Replicas) > 0

	if p.err != nil {
		img.Err = p.err.Error()
		compareReplicas(&img)
		img.Statuses = applicable(img)
		img.Status = img.Statuses[0]
		return img
	}

	img.RegistryDigest = p.image.Digest
	img.RegistryCreated = p.image.Created
	img.RegistryRevision = p.image.Revision()
	img.Created = p.image.Created
	img.Revision = p.image.Revision()
	img.Source = p.image.Source()

	// A ShapeTagDigest reference's single Inspect above is pinned to the
	// declared digest, so it can never describe what the tag currently
	// serves. When the separate tag-current fetch succeeded, it -- not the
	// declared-digest fetch -- describes the registry side.
	if p.hasRegistryImage {
		img.RegistryDigest = p.registryImage.Digest
		img.RegistryCreated = p.registryImage.Created
		img.RegistryRevision = p.registryImage.Revision()
	}

	// Release notes need a GitHub repository: the label, else config.
	img.NotesRepo = notes.RepoFromSource(img.Source)
	if override := cfg.Image(st.Name, svc.Ref.Repository).Repo; override != "" {
		img.NotesRepo = override
	}

	// The current version is the tag, or the label for a digest-only pin.
	img.Version = svc.Ref.Tag
	if svc.Ref.Shape == imageref.ShapeDigestOnly {
		img.Version = p.image.Version()
	}

	// Re-derive the constraint now that a digest-only pin has a version.
	glob := cfg.Image(st.Name, svc.Ref.Repository).Constraint
	c := policy.Infer(img.Version)
	if glob != "" {
		c = policy.Parse(glob)
	}

	if img.Version != "" && c.Trackable {
		res := policy.Evaluate(img.Version, p.tags, c)
		img.Candidate = res.Candidate
		img.Kind = res.Kind
		img.Ordered = res.Ordered
		if res.Kind != policy.KindNone {
			img.KindName = res.Kind.String()
		}
		// A trackable constraint that matched nothing never actually ran
		// the check the user configured; reporting "current" here would be
		// a wrong answer presented as authoritative.
		if len(res.Ordered) == 0 {
			desc := glob
			if desc == "" {
				desc = fmt.Sprintf("inferred from %s", img.Version)
			}
			img.Err = fmt.Sprintf("no tags matched the constraint (%s)", desc)
		}
	}

	compareReplicas(&img)
	img.Statuses = applicable(img)
	img.Status = img.Statuses[0]
	return img
}
