// Package registry talks to container registries. It is one of only three
// packages permitted to perform I/O against the outside world.
package registry

import (
	"context"
	"fmt"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

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
// reference may carry a tag, a digest, or both.
func (c *Client) Inspect(ctx context.Context, ref string) (Image, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return Image{}, fmt.Errorf("registry: parsing %s: %w", ref, err)
	}

	desc, err := remote.Get(parsed, c.options(ctx)...)
	if err != nil {
		return Image{}, fmt.Errorf("registry: fetching %s: %w", ref, err)
	}

	// Image() resolves a manifest list to the matching platform for us.
	img, err := desc.Image()
	if err != nil {
		return Image{}, fmt.Errorf("registry: resolving image %s: %w", ref, err)
	}

	digest, err := img.Digest()
	if err != nil {
		return Image{}, fmt.Errorf("registry: digesting %s: %w", ref, err)
	}

	cf, err := img.ConfigFile()
	if err != nil {
		return Image{}, fmt.Errorf("registry: reading config of %s: %w", ref, err)
	}

	return Image{
		Digest:  digest.String(),
		Created: cf.Created.Time,
		Labels:  cf.Config.Labels,
	}, nil
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
