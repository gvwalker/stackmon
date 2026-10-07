// Package selfupdate checks for and installs newer stackmon releases from
// GitHub. It is one of only four packages permitted to perform I/O against
// the outside world.
package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/google/go-github/v66/github"
)

// Repo is the stackmon GitHub repository.
const Repo = "gvwalker/stackmon"

// Release is a stackmon release.
type Release struct {
	Tag  string
	Body string
	URL  string
}

// asset is one downloadable file on a release.
type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	// Digest is "sha256:<hex>" for the asset's content: the same hash the
	// release page shows, computed and served by GitHub. go-github's
	// ReleaseAsset has no field for it at any published version, so a
	// release is read as it arrives rather than through that type.
	Digest string `json:"digest"`
}

// release is the part of a GitHub release stackmon reads.
type release struct {
	Tag     string  `json:"tag_name"`
	Body    string  `json:"body"`
	HTMLURL string  `json:"html_url"`
	Assets  []asset `json:"assets"`
}

// Client checks for and installs stackmon releases.
type Client struct {
	gh *github.Client
	hc *http.Client
}

// New returns a Client. An empty token means unauthenticated access, which
// GitHub rate-limits to 60 requests per hour.
func New(token string) *Client {
	gh := github.NewClient(nil)
	if token != "" {
		gh = gh.WithAuthToken(token)
	}
	// STACKMON_GITHUB_API names an alternate API root: GitHub Enterprise, a
	// mirror, or a test fixture. An unusable value is ignored rather than
	// fatal -- a typo must not make an update check report the running
	// version as current.
	if raw := os.Getenv("STACKMON_GITHUB_API"); raw != "" {
		if u, err := url.Parse(strings.TrimSuffix(raw, "/") + "/"); err == nil {
			gh.BaseURL = u
		}
	}
	return &Client{gh: gh, hc: http.DefaultClient}
}

// Latest returns the newest published release and whether it is newer than
// current. current is a build-time version like "v1.2.3"; a version that
// doesn't parse as semver (e.g. the "dev" build) never counts as newer.
func (c *Client) Latest(ctx context.Context, current string) (Release, bool, error) {
	owner, name, _ := strings.Cut(Repo, "/")
	r, _, err := c.gh.Repositories.GetLatestRelease(ctx, owner, name)
	if err != nil {
		return Release{}, false, fmt.Errorf("selfupdate: fetching latest release: %w", err)
	}
	rel := Release{Tag: r.GetTagName(), Body: r.GetBody(), URL: r.GetHTMLURL()}

	curV, err1 := semver.NewVersion(strings.TrimPrefix(current, "v"))
	latV, err2 := semver.NewVersion(strings.TrimPrefix(rel.Tag, "v"))
	if err1 != nil || err2 != nil {
		return rel, false, nil
	}
	return rel, latV.Compare(curV) > 0, nil
}

// ByTag returns the release notes for a specific tag.
func (c *Client) ByTag(ctx context.Context, tag string) (Release, error) {
	r, err := c.release(ctx, tag)
	if err != nil {
		return Release{}, err
	}
	return Release{Tag: r.Tag, Body: r.Body, URL: r.HTMLURL}, nil
}

// release reads one release. It goes through go-github's own authenticated,
// rate-limit-checked request plumbing and then decodes the body itself, so
// the token, the STACKMON_GITHUB_API base URL, and the secondary-rate-limit
// handling all behave exactly as they do for the typed calls.
func (c *Client) release(ctx context.Context, tag string) (release, error) {
	owner, name, _ := strings.Cut(Repo, "/")
	req, err := c.gh.NewRequest(http.MethodGet,
		fmt.Sprintf("repos/%s/%s/releases/tags/%s", owner, name, tag), nil)
	if err != nil {
		return release{}, fmt.Errorf("selfupdate: fetching release %s: %w", tag, err)
	}
	resp, err := c.gh.BareDo(ctx, req)
	if err != nil {
		return release{}, fmt.Errorf("selfupdate: fetching release %s: %w", tag, err)
	}
	defer resp.Body.Close()

	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return release{}, fmt.Errorf("selfupdate: reading release %s: %w", tag, err)
	}
	return rel, nil
}

// Install downloads the release asset for the running OS/arch, verifies it
// against the digest GitHub reports for that asset, and replaces the running
// binary in place.
func (c *Client) Install(ctx context.Context, tag string) error {
	r, err := c.release(ctx, tag)
	if err != nil {
		return err
	}

	assetName := fmt.Sprintf("stackmon-%s-%s", runtime.GOOS, runtime.GOARCH)
	var assetURL, digest string
	for _, a := range r.Assets {
		if a.Name == assetName {
			assetURL, digest = a.URL, a.Digest
		}
	}
	if assetURL == "" {
		return fmt.Errorf("selfupdate: release %s has no asset for %s/%s", tag, runtime.GOOS, runtime.GOARCH)
	}
	// No digest means nothing to verify against. Refusing is the whole
	// point: installing the download because the check was impossible is
	// exactly the outcome the check exists to prevent.
	want, ok := strings.CutPrefix(digest, "sha256:")
	if !ok || want == "" {
		return fmt.Errorf("selfupdate: release %s reports no sha256 digest for %s, so it cannot be verified", tag, assetName)
	}

	data, err := c.fetch(ctx, assetURL)
	if err != nil {
		return fmt.Errorf("selfupdate: downloading %s: %w", assetName, err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != want {
		return fmt.Errorf("selfupdate: checksum mismatch for %s", assetName)
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("selfupdate: locating running binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return install(exe, data)
}

func (c *Client) fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// install atomically replaces the file at path with data. Writing to a
// temp file in the same directory then renaming over path means a process
// still running the old binary keeps its already-open inode, and a reader
// of path never observes a partially-written file.
func install(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".stackmon-update-*")
	if err != nil {
		return fmt.Errorf("selfupdate: creating temp file: %w", err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("selfupdate: writing new binary: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return fmt.Errorf("selfupdate: chmod new binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("selfupdate: writing new binary: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("selfupdate: installing new binary: %w", err)
	}
	return nil
}
