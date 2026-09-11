// Package selfupdate checks for and installs newer stackmon releases from
// GitHub. It is one of only four packages permitted to perform I/O against
// the outside world.
package selfupdate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	return &Client{gh: gh, hc: http.DefaultClient}
}

// NewWithBaseURL points the client at an alternate API root, for tests.
func NewWithBaseURL(token, baseURL string) (*Client, error) {
	c := New(token)
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("selfupdate: parsing base URL: %w", err)
	}
	c.gh.BaseURL = u
	return c, nil
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
	return Release{Tag: r.GetTagName(), Body: r.GetBody(), URL: r.GetHTMLURL()}, nil
}

func (c *Client) release(ctx context.Context, tag string) (*github.RepositoryRelease, error) {
	owner, name, _ := strings.Cut(Repo, "/")
	r, _, err := c.gh.Repositories.GetReleaseByTag(ctx, owner, name, tag)
	if err != nil {
		return nil, fmt.Errorf("selfupdate: fetching release %s: %w", tag, err)
	}
	return r, nil
}

// Install downloads the release asset for the running OS/arch, verifies its
// checksum against the release's checksums.txt, and replaces the running
// binary in place.
func (c *Client) Install(ctx context.Context, tag string) error {
	r, err := c.release(ctx, tag)
	if err != nil {
		return err
	}

	assetName := fmt.Sprintf("stackmon-%s-%s", runtime.GOOS, runtime.GOARCH)
	var assetURL, sumsURL string
	for _, a := range r.Assets {
		switch a.GetName() {
		case assetName:
			assetURL = a.GetBrowserDownloadURL()
		case "checksums.txt":
			sumsURL = a.GetBrowserDownloadURL()
		}
	}
	if assetURL == "" {
		return fmt.Errorf("selfupdate: release %s has no asset for %s/%s", tag, runtime.GOOS, runtime.GOARCH)
	}
	if sumsURL == "" {
		return fmt.Errorf("selfupdate: release %s has no checksums.txt", tag)
	}

	sums, err := c.fetch(ctx, sumsURL)
	if err != nil {
		return fmt.Errorf("selfupdate: downloading checksums: %w", err)
	}
	want, ok := parseChecksums(sums)[assetName]
	if !ok {
		return fmt.Errorf("selfupdate: checksums.txt has no entry for %s", assetName)
	}

	data, err := c.fetch(ctx, assetURL)
	if err != nil {
		return fmt.Errorf("selfupdate: downloading %s: %w", assetName, err)
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != want {
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

// parseChecksums reads a "sha256sum <path>" checksums.txt into name -> sum.
func parseChecksums(text []byte) map[string]string {
	out := map[string]string{}
	s := bufio.NewScanner(strings.NewReader(string(text)))
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) != 2 {
			continue
		}
		out[filepath.Base(fields[1])] = fields[0]
	}
	return out
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
