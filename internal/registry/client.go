// Package registry talks to container registries. It is one of only four
// packages permitted to perform I/O against the outside world.
package registry

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// DefaultPlatform is the platform inspected for a Service that declares none.
// It is go-containerregistry's own default, so a stack that says nothing is
// inspected exactly as it always was; stackmon has no better source than the
// library does, and guessing from the machine stackmon runs on would report
// darwin metadata for a linux stack.
func DefaultPlatform() v1.Platform { return v1.Platform{OS: "linux", Architecture: "amd64"} }

// Target returns the platform to inspect a reference at: the one Compose
// declares for its Service, or the default when it declares none. A declared
// value stackmon cannot parse is an error rather than a silent fallback,
// because falling back reads some other platform's metadata and reports it as
// the declared one.
func Target(declared string) (v1.Platform, error) {
	if strings.TrimSpace(declared) == "" {
		return DefaultPlatform(), nil
	}
	p, err := v1.ParsePlatform(declared)
	if err != nil {
		return v1.Platform{}, fmt.Errorf("registry: parsing platform %q: %w", declared, err)
	}
	return *p, nil
}

// OCI label keys stackmon reads.
const (
	LabelVersion  = "org.opencontainers.image.version"
	LabelRevision = "org.opencontainers.image.revision"
	LabelSource   = "org.opencontainers.image.source"
)

// Image is the subset of an image's metadata stackmon needs.
type Image struct {
	Digest  string
	Created time.Time
	Labels  map[string]string
}

// Label returns a label value, or "" when absent.
func (i Image) Label(key string) string { return i.Labels[key] }

// Version is org.opencontainers.image.version. Verified present on traefik
// and linuxserver images, absent on cloudflared and Docker Official Images.
func (i Image) Version() string { return i.Label(LabelVersion) }

// Revision is org.opencontainers.image.revision, the upstream commit.
func (i Image) Revision() string { return i.Label(LabelRevision) }

// Source is org.opencontainers.image.source, the upstream repository URL.
func (i Image) Source() string { return i.Label(LabelSource) }

// Client fetches image metadata and tag lists.
type Client struct {
	keychain authn.Keychain
}

// New returns a Client authenticating from ~/.docker/config.json, including
// credential helpers, and falling back to anonymous access.
func New() *Client {
	return &Client{keychain: authn.DefaultKeychain}
}

func (c *Client) options(ctx context.Context) []remote.Option {
	return []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(c.keychain),
	}
}

// Inspect fetches the manifest digest and config blob for a reference. The
// reference may carry a tag, a digest, or both. When it names a
// multi-platform index, the config blob is read from the child serving
// platform.
func (c *Client) Inspect(ctx context.Context, ref string, platform v1.Platform) (Image, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return Image{}, fmt.Errorf("registry: parsing %s: %w", ref, err)
	}

	opts := append(c.options(ctx), remote.WithPlatform(platform))
	desc, err := remote.Get(parsed, opts...)
	if err != nil {
		return Image{}, fmt.Errorf("registry: fetching %s: %w", ref, err)
	}

	// desc.Digest is what was actually fetched: the index digest when ref
	// names a multi-platform index, matching what `docker pull` prints and
	// what a compose @sha256 pin and RepoDigests hold. Image() below
	// resolves the index to one platform's child purely to read its config
	// blob; reporting that child's digest instead would make every
	// multi-arch digest-pinned image look permanently drifted, and would
	// have bump rewrite a portable multi-arch pin into a platform-specific
	// one. So the platform selects the config blob and nothing else.
	img, err := desc.Image()
	if err != nil {
		if desc.MediaType.IsIndex() {
			err = unpublishedPlatform(desc, platform)
		}
		return Image{}, fmt.Errorf("registry: resolving image %s: %w", ref, err)
	}

	cf, err := img.ConfigFile()
	if err != nil {
		return Image{}, fmt.Errorf("registry: reading config of %s: %w", ref, err)
	}

	return Image{
		Digest:  desc.Digest.String(),
		Created: cf.Created.Time,
		Labels:  cf.Config.Labels,
	}, nil
}

// unpublishedPlatform explains a failed index-to-child resolution. The
// library reports which platform it could not find and never which ones
// exist, and "no child with platform {amd64 arm64 <nil> <nil> []}" leaves the
// reader with nothing to act on: it is the difference between an ARM-only
// image (nothing is wrong) and the wrong platform (the compose file is). The
// library's own wording is dropped rather than wrapped, since the rewritten
// one names the same platform far more usefully. A fetch that fails again
// here costs nothing: the row is already unknown.
func unpublishedPlatform(desc *remote.Descriptor, want v1.Platform) error {
	idx, err := desc.ImageIndex()
	if err != nil {
		return fmt.Errorf("no image for platform %s", want)
	}
	manifest, err := idx.IndexManifest()
	if err != nil {
		return fmt.Errorf("no image for platform %s", want)
	}
	var published []string
	for _, child := range manifest.Manifests {
		if child.Platform != nil {
			published = append(published, child.Platform.String())
		}
	}
	if len(published) == 0 {
		return fmt.Errorf("no image for platform %s: the index publishes no platform", want)
	}
	return fmt.Errorf("no image for platform %s: the index publishes %s", want, strings.Join(published, ", "))
}

// Tags lists every tag in a repository.
func (c *Client) Tags(ctx context.Context, repo string) ([]string, error) {
	parsed, err := name.NewRepository(repo)
	if err != nil {
		return nil, fmt.Errorf("registry: parsing repository %s: %w", repo, err)
	}
	tags, err := remote.List(parsed, c.options(ctx)...)
	if err != nil {
		return nil, fmt.Errorf("registry: listing tags for %s: %w", repo, err)
	}
	return tags, nil
}
