package report

import (
	"context"
	"fmt"
	"sort"
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

// RegistryProber is the registry surface Build depends on, so that report
// building is testable without network access.
type RegistryProber interface {
	Inspect(ctx context.Context, ref string) (registry.Image, error)
	Tags(ctx context.Context, repo string) ([]string, error)
}

// Options configures a run.
type Options struct {
	Registry    RegistryProber
	Docker      local.Prober
	Config      config.Config
	Concurrency int
}

// probe is what one unique reference resolves to, shared by every service
// that declares it.
type probe struct {
	image registry.Image
	tags  []string
	err   error
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

// Build probes every image in every stack and assembles the report. It never
// returns an error: a per-image failure becomes an unknown row, so one
// unreachable registry cannot fail the run.
func Build(ctx context.Context, stacks []compose.Stack, opts Options) Report {
	if opts.Concurrency < 1 {
		opts.Concurrency = config.DefaultConcurrency
	}

	r := Report{Generated: time.Now().UTC()}

	// The running-container signal is optional.
	running := map[string]local.Container{}
	if opts.Docker != nil && opts.Docker.Available() {
		if cs, err := opts.Docker.Containers(ctx); err == nil {
			r.DockerAvailable = true
			running = local.Index(cs)
		}
	}

	// Deduplicate: the same reference in two stacks is probed once.
	unique := map[string]imageref.Ref{}
	for _, st := range stacks {
		for _, svc := range st.Services {
			unique[svc.Ref.Resolved] = svc.Ref
		}
	}

	// Constraints depend on config, which is per stack, so collect the set of
	// constraints each reference needs before probing.
	constraints := map[string]policy.Constraint{}
	for _, st := range stacks {
		for _, svc := range st.Services {
			c := constraintFor(opts.Config, st.Name, svc.Ref)
			// A trackable constraint anywhere means tags must be listed.
			if c.Trackable || !constraints[svc.Ref.Resolved].Trackable {
				constraints[svc.Ref.Resolved] = c
			}
		}
	}

	probes := make(map[string]probe, len(unique))
	var mu sync.Mutex

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(opts.Concurrency)

	for resolved, ref := range unique {
		resolved, ref := resolved, ref
		g.Go(func() error {
			p := probe{}
			p.image, p.err = opts.Registry.Inspect(gctx, resolved)

			// A tag+digest pin's single fetch above is pinned to the
			// declared digest, so it can never see what the tag currently
			// serves. Inspect the bare tag separately so RegistryDigest
			// describes the registry's current state, not the declared
			// one. Other shapes have no separate tag to check this way:
			// a tag-only ref's fetch above is already the current tag, and
			// a digest-only ref names no tag at all.
			if p.err == nil && ref.Shape == imageref.ShapeTagDigest {
				tagRef := ref.Registry + "/" + ref.Repository + ":" + ref.Tag
				img, err := opts.Registry.Inspect(gctx, tagRef)
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
				needsTags := constraints[resolved].Trackable
				if needsTags {
					repo := ref.Registry + "/" + ref.Repository
					tags, err := opts.Registry.Tags(gctx, repo)
					if err != nil {
						p.err = err
					} else {
						p.tags = tags
					}
				}
			}

			mu.Lock()
			probes[resolved] = p
			mu.Unlock()
			return nil // Per-image failures are data, not run failures.
		})
	}
	_ = g.Wait() // No goroutine returns an error.

	for _, st := range stacks {
		for _, svc := range st.Services {
			r.Images = append(r.Images, assemble(st, svc, probes[svc.Ref.Resolved], running, r.DockerAvailable, opts.Config))
		}
	}

	// Second pass: resolve each unique candidate's digest, so bump can pair
	// an advancing tag with the correct new digest instead of the declared
	// reference's stale one. RegistryDigest above describes only the
	// declared reference and is never the candidate's digest. A failure
	// here must not fail the run or affect Status: only the bump path is
	// missing information.
	candidateKeys := map[string]struct{}{}
	for _, img := range r.Images {
		if img.Candidate == "" {
			continue
		}
		candidateKeys[img.Ref.Registry+"/"+img.Ref.Repository+":"+img.Candidate] = struct{}{}
	}

	if len(candidateKeys) > 0 {
		digests := make(map[string]string, len(candidateKeys))
		var dmu sync.Mutex

		cg, cgctx := errgroup.WithContext(ctx)
		cg.SetLimit(opts.Concurrency)
		for key := range candidateKeys {
			key := key
			cg.Go(func() error {
				if img, err := opts.Registry.Inspect(cgctx, key); err == nil {
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
			key := r.Images[i].Ref.Registry + "/" + r.Images[i].Ref.Repository + ":" + r.Images[i].Candidate
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

func assemble(st compose.Stack, svc compose.Service, p probe, running map[string]local.Container, dockerUp bool, cfg config.Config) Image {
	img := Image{
		Stack:          st.Name,
		Service:        svc.Name,
		Ref:            svc.Ref,
		DeclaredDigest: svc.Ref.Digest,
		DockerChecked:  dockerUp,
	}

	if c, ok := running[st.Name+"/"+svc.Name]; ok {
		img.Running = true
		img.RunningDigests = c.RepoDigests
	}

	if p.err != nil {
		img.Err = p.err.Error()
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

	img.Statuses = applicable(img)
	img.Status = img.Statuses[0]
	return img
}
