// Package notes fetches release notes from GitHub. It is one of only three
// packages permitted to perform I/O against the outside world.
package notes

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/google/go-github/v66/github"
)

// Release is one upstream release.
type Release struct {
	Tag       string
	Name      string
	Body      string
	URL       string
	Published time.Time
}

// Fetcher is the surface report building depends on, so that it can be tested
// without network access.
type Fetcher interface {
	Between(ctx context.Context, repo, from, to string) ([]Release, error)
}

// Client fetches releases from GitHub.
type Client struct {
	gh *github.Client
}

// New returns a Client. An empty token means unauthenticated access, which
// GitHub rate-limits to 60 requests per hour.
func New(token string) *Client {
	gh := github.NewClient(nil)
	if token != "" {
		gh = gh.WithAuthToken(token)
	}
	return &Client{gh: gh}
}

// NewWithBaseURL points the client at an alternate API root, for tests.
func NewWithBaseURL(token, baseURL string) (*Client, error) {
	c := New(token)
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("notes: parsing base URL: %w", err)
	}
	c.gh.BaseURL = u
	return c, nil
}

// RepoFromSource converts an org.opencontainers.image.source URL into
// "owner/name". Non-GitHub forges yield "", so callers degrade to "no notes
// available" rather than failing.
func RepoFromSource(source string) string {
	u, err := url.Parse(strings.TrimSpace(source))
	if err != nil || u.Host != "github.com" {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")
}

// Between returns releases newer than from and no newer than to, oldest
// first. Tags are compared by semver so that a "v" prefix mismatch between
// the image tag and the GitHub tag does not hide releases.
func Between(releases []Release, from, to string) []Release {
	fromVer, fromOK := parseVersion(from)
	toVer, toOK := parseVersion(to)
	if !fromOK || !toOK {
		return nil
	}

	var out []Release
	for _, r := range releases {
		v, ok := parseVersion(r.Tag)
		if !ok {
			continue
		}
		if v.Compare(fromVer) > 0 && v.Compare(toVer) <= 0 {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := parseVersion(out[i].Tag)
		b, _ := parseVersion(out[j].Tag)
		return a.Compare(b) < 0
	})
	return out
}

// Between fetches a repository's releases and filters them to the range.
func (c *Client) Between(ctx context.Context, repo, from, to string) ([]Release, error) {
	owner, name, found := strings.Cut(repo, "/")
	if !found {
		return nil, fmt.Errorf("notes: %q is not an owner/name repository", repo)
	}

	var all []Release
	opts := &github.ListOptions{PerPage: 100}
	for page := 0; page < 3; page++ { // Cap the walk; three pages is 300 releases.
		rels, resp, err := c.gh.Repositories.ListReleases(ctx, owner, name, opts)
		if err != nil {
			return nil, fmt.Errorf("notes: listing releases for %s: %w", repo, err)
		}
		for _, r := range rels {
			if r.GetPrerelease() || r.GetDraft() {
				continue
			}
			all = append(all, Release{
				Tag:       r.GetTagName(),
				Name:      r.GetName(),
				Body:      r.GetBody(),
				URL:       r.GetHTMLURL(),
				Published: r.GetPublishedAt().Time,
			})
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return Between(all, from, to), nil
}

// parseVersion tolerates a leading "v" and trailing variant suffixes.
func parseVersion(tag string) (*semver.Version, bool) {
	v, err := semver.NewVersion(strings.TrimPrefix(strings.TrimSpace(tag), "v"))
	if err != nil {
		return nil, false
	}
	return v, true
}
