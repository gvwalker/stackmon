// Package ghclient builds the GitHub clients stackmon talks to, so every
// caller gets the same authentication, the same alternate API root, and the
// same request budgets. It only constructs clients; it performs no I/O
// itself, which keeps it outside the four packages permitted to touch the
// outside world.
package ghclient

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/go-github/v66/github"

	"github.com/gvwalker/stackmon/internal/readretry"
)

// Request budgets. Each bounds one HTTP exchange end to end -- connecting,
// waiting for headers, retries/backoff, and reading the whole body -- because a
// server that answers and then stalls is as unreachable as one that never
// answers at all. They are ceilings, not extensions: an earlier caller
// deadline or cancellation always wins, and cancellation is honored mid-body.
var (
	// Metadata is the budget for one GitHub API request: a release
	// listing, a tagged release, the latest release. The documents are
	// small, so anything that outlives this is a stall, not slowness.
	Metadata = 30 * time.Second
	// Download is the budget for one release-asset download: a stackmon
	// executable, megabytes where the API documents are kilobytes. Bounded
	// all the same -- an update that cannot finish must fail, not hang.
	Download = 10 * time.Minute
)

// New returns a go-github client for release metadata, configured with the
// token and API-root policy every caller shares: an empty token means
// unauthenticated access, which GitHub rate-limits to 60 requests per hour,
// and STACKMON_GITHUB_API names an alternate API root (GitHub Enterprise, a
// mirror, or a test fixture). An unusable value is ignored rather than fatal
// -- a typo in an environment variable must neither stop a report nor let
// an update check claim the running version is current. Every request the
// client makes is bounded by Metadata.
func New(token string) *github.Client {
	gh := github.NewClient(&http.Client{Timeout: Metadata, Transport: readretry.Transport(nil)})
	if token != "" {
		gh = gh.WithAuthToken(token)
	}
	if raw := os.Getenv("STACKMON_GITHUB_API"); raw != "" {
		if u, err := url.Parse(strings.TrimSuffix(raw, "/") + "/"); err == nil {
			gh.BaseURL = u
		}
	}
	return gh
}

// Downloader returns the HTTP client for release assets, bounded by
// Download: the same end-to-end exchange budget as the API client, sized
// for an executable instead of a JSON document.
func Downloader() *http.Client {
	return &http.Client{Timeout: Download, Transport: readretry.Transport(nil)}
}
