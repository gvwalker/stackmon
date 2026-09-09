package registry

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	ggcrregistry "github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// fakeRegistry starts an in-memory registry and returns its host:port.
func fakeRegistry(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(ggcrregistry.New())
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

// anonymousKeychain always resolves to authn.Anonymous, so these tests
// never depend on the machine's real Docker credential configuration: no
// network is involved, but authn.DefaultKeychain still reads
// ~/.docker/config.json and can shell out to a credential helper, which
// could make an otherwise-hermetic test fail or hang for unrelated
// reasons.
type anonymousKeychain struct{}

func (anonymousKeychain) Resolve(authn.Resource) (authn.Authenticator, error) {
	return authn.Anonymous, nil
}

func testClient() *Client { return newClient(anonymousKeychain{}) }

// push publishes an image with the given labels and creation time.
func push(t *testing.T, ref string, created time.Time, labels map[string]string) v1.Hash {
	t.Helper()
	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cf = cf.DeepCopy()
	cf.Created = v1.Time{Time: created}
	cf.Config.Labels = labels
	img, err = mutate.ConfigFile(img, cf)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(parsed, img); err != nil {
		t.Fatal(err)
	}
	h, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestInspectReturnsDigestCreatedAndLabels(t *testing.T) {
	host := fakeRegistry(t)
	created := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	want := push(t, host+"/traefik:v3.7.10", created, map[string]string{
		"org.opencontainers.image.version": "v3.7.10",
		"org.opencontainers.image.source":  "https://github.com/traefik/traefik",
	})

	got, err := testClient().Inspect(context.Background(), host+"/traefik:v3.7.10")
	if err != nil {
		t.Fatalf("Inspect error: %v", err)
	}
	if got.Digest != want.String() {
		t.Errorf("Digest = %q, want %q", got.Digest, want.String())
	}
	if !got.Created.Equal(created) {
		t.Errorf("Created = %v, want %v", got.Created, created)
	}
	if got.Version() != "v3.7.10" {
		t.Errorf("Version() = %q, want v3.7.10", got.Version())
	}
	if got.Source() != "https://github.com/traefik/traefik" {
		t.Errorf("Source() = %q", got.Source())
	}
}

// The digest-only pin is the case the whole traefik design depends on.
func TestInspectResolvesDigestOnlyReference(t *testing.T) {
	host := fakeRegistry(t)
	digest := push(t, host+"/traefik:v3.7.10", time.Now(), map[string]string{
		"org.opencontainers.image.version": "v3.7.10",
	})

	got, err := testClient().Inspect(context.Background(), host+"/traefik@"+digest.String())
	if err != nil {
		t.Fatalf("Inspect error: %v", err)
	}
	if got.Version() != "v3.7.10" {
		t.Errorf("Version() = %q, want v3.7.10 recovered from a bare digest", got.Version())
	}
}

func TestInspectMissingLabelsYieldsEmptyStrings(t *testing.T) {
	host := fakeRegistry(t)
	push(t, host+"/postgres:18-alpine", time.Now(), nil)

	got, err := testClient().Inspect(context.Background(), host+"/postgres:18-alpine")
	if err != nil {
		t.Fatalf("Inspect error: %v", err)
	}
	if got.Version() != "" || got.Source() != "" {
		t.Errorf("unlabelled image reported Version=%q Source=%q, want empty", got.Version(), got.Source())
	}
}

// REPRODUCED (Critical finding): Inspect must report the digest of what was
// actually fetched. For a multi-platform index that is the index digest --
// what `docker pull` prints and what a compose @sha256 pin and RepoDigests
// hold -- never the platform-matching child's digest that Image() resolves
// to internally to read the config blob. Reporting the child's digest makes
// every multi-arch digest-pinned image look permanently drifted, and would
// have bump rewrite a portable multi-arch pin into a platform-specific one.
func TestInspectReportsIndexDigestForMultiArchImage(t *testing.T) {
	host := fakeRegistry(t)

	amd64Img, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	arm64Img, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}

	idx := mutate.AppendManifests(empty.Index,
		mutate.IndexAddendum{
			Add:        amd64Img,
			Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "amd64"}},
		},
		mutate.IndexAddendum{
			Add:        arm64Img,
			Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "arm64"}},
		},
	)

	indexDigest, err := idx.Digest()
	if err != nil {
		t.Fatal(err)
	}
	amdDigest, err := amd64Img.Digest()
	if err != nil {
		t.Fatal(err)
	}

	ref := host + "/multiarch:v1"
	parsed, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(parsed, idx, remote.WithContext(context.Background())); err != nil {
		t.Fatal(err)
	}

	got, err := testClient().Inspect(context.Background(), ref)
	if err != nil {
		t.Fatalf("Inspect error: %v", err)
	}
	if got.Digest != indexDigest.String() {
		t.Errorf("Digest = %q, want the index digest %q", got.Digest, indexDigest.String())
	}
	if got.Digest == amdDigest.String() {
		t.Fatal("Digest equals the amd64 child's digest, not the index's, for a multi-platform reference")
	}
}

func TestTagsListsRepositoryTags(t *testing.T) {
	host := fakeRegistry(t)
	for _, tag := range []string{"1.0.0", "1.1.0", "1.2.0"} {
		push(t, host+"/app:"+tag, time.Now(), nil)
	}

	got, err := testClient().Tags(context.Background(), host+"/app")
	if err != nil {
		t.Fatalf("Tags error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Tags = %v, want 3 entries", got)
	}
	joined := strings.Join(got, ",")
	for _, want := range []string{"1.0.0", "1.1.0", "1.2.0"} {
		if !strings.Contains(joined, want) {
			t.Errorf("Tags = %v, missing %q", got, want)
		}
	}
}

func TestInspectUnknownReferenceErrors(t *testing.T) {
	host := fakeRegistry(t)
	if _, err := testClient().Inspect(context.Background(), host+"/nope:missing"); err == nil {
		t.Fatal("Inspect of absent image = nil error, want error")
	}
}

func TestInspectRespectsContextCancellation(t *testing.T) {
	host := fakeRegistry(t)
	push(t, host+"/app:1.0.0", time.Now(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := testClient().Inspect(ctx, host+"/app:1.0.0"); err == nil {
		t.Fatal("Inspect with cancelled context = nil error, want error")
	}
}
