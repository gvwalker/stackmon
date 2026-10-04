// Package local reads running-container state from the Docker Engine API over
// its unix socket. It is one of only four packages permitted to perform I/O
// against the outside world.
//
// The Engine API is queried directly rather than through the official client
// library, which would add a very large dependency tree for a single
// endpoint.
package local

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// DefaultSocket is the conventional Docker socket path.
const DefaultSocket = "/var/run/docker.sock"

// Compose sets these labels on every container it starts.
const (
	labelProject            = "com.docker.compose.project"
	labelProjectWorkingDir  = "com.docker.compose.project.working_dir"
	labelProjectConfigFiles = "com.docker.compose.project.config_files"
	labelService            = "com.docker.compose.service"
	labelOneOff             = "com.docker.compose.oneoff"
)

// Container is one running container started by Compose.
type Container struct {
	Project            string
	ProjectWorkingDir  string
	ProjectConfigFiles string
	Service            string
	// Image is the reference the container was started from.
	Image string
	// RepoDigests is every manifest digest Docker has recorded for the
	// image actually in use, resolved from the daemon's ImageID via
	// GET /images/{id}/json's RepoDigests. This is a different namespace
	// from Docker's own ImageID, which hashes the local image config
	// JSON, not the registry manifest: comparing ImageID directly
	// against a compose @sha256 pin or a registry.Inspect result
	// compares two unrelated hashes. Docker records one entry per
	// manifest digest an image has ever been pulled under, and the same
	// repository can legitimately appear more than once (e.g. a retag
	// under a new digest); treating only the first entry as "the"
	// digest produces false not-deployed verdicts for images running
	// exactly the declared digest. Empty when the image has no
	// RepoDigests entry, e.g. built locally and never pushed.
	RepoDigests []string
	// DigestError is why RepoDigests stayed empty when it did: a failed
	// daemon lookup, not a legitimately digest-less image.
	DigestError string
	// OneOff marks a container started by `docker compose run`. Such a
	// container is a finished job, not a Replica of a running Service, so
	// consumers that reason about a project's up state must skip it.
	OneOff bool
}

// Client queries the Docker Engine API.
type Client struct {
	socket string
	http   *http.Client
}

// New returns a Client using the default socket path. STACKMON_DOCKER_SOCKET
// overrides it, for daemons that do not listen on the conventional path
// (rootless Docker, a non-standard socket).
func New() *Client {
	if path := os.Getenv("STACKMON_DOCKER_SOCKET"); path != "" {
		return NewWithSocket(path)
	}
	return NewWithSocket(DefaultSocket)
}

// NewWithSocket returns a Client using an explicit socket path.
func NewWithSocket(path string) *Client {
	return &Client{
		socket: path,
		http: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", path)
				},
			},
		},
	}
}

// Available reports whether the socket exists and is usable. When it is not,
// callers omit the running-container signal rather than failing.
func (c *Client) Available() bool {
	st, err := os.Stat(c.socket)
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeSocket != 0
}

// apiContainer mirrors the fields stackmon reads from GET /containers/json.
type apiContainer struct {
	Image   string            `json:"Image"`
	ImageID string            `json:"ImageID"`
	Labels  map[string]string `json:"Labels"`
}

// apiImageInspect mirrors the fields stackmon reads from
// GET /images/{id}/json.
type apiImageInspect struct {
	RepoDigests []string `json:"RepoDigests"`
}

// Containers lists running containers started by Compose. Containers without
// Compose labels are omitted, since they cannot be matched to a service.
// One-off `docker compose run` containers are included and flagged, because
// whether one counts is the caller's decision, not the reader's.
func (c *Client) Containers(ctx context.Context) ([]Container, error) {
	// The host is ignored by the unix dialer but required by net/http.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/v1.44/containers/json", nil)
	if err != nil {
		return nil, fmt.Errorf("local: building request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("local: querying docker at %s: %w", c.socket, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("local: docker returned %s: %s", resp.Status, body)
	}

	var raw []apiContainer
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("local: decoding docker response: %w", err)
	}

	out := make([]Container, 0, len(raw))
	// Cached per ImageID within this call, so N containers sharing an
	// image cost one extra request, not N.
	digests := map[string][]string{}
	digestErrs := map[string]error{}
	for _, r := range raw {
		project, service := r.Labels[labelProject], r.Labels[labelService]
		if project == "" || service == "" {
			continue
		}

		var digs []string
		var inspectErr error
		if r.ImageID != "" {
			d, ok := digests[r.ImageID]
			if !ok {
				d, inspectErr = c.repoDigests(ctx, r.ImageID)
				digestErrs[r.ImageID] = inspectErr
				digests[r.ImageID] = d
			} else {
				inspectErr = digestErrs[r.ImageID]
			}
			digs = d
		}

		cont := Container{
			Project:            project,
			ProjectWorkingDir:  r.Labels[labelProjectWorkingDir],
			ProjectConfigFiles: r.Labels[labelProjectConfigFiles],
			Service:            service,
			Image:              r.Image,
			RepoDigests:        digs,
			OneOff:             strings.EqualFold(r.Labels[labelOneOff], "true"),
		}
		if inspectErr != nil {
			cont.DigestError = inspectErr.Error()
		}
		out = append(out, cont)
	}
	return out, nil
}

// repoDigests resolves imageID's manifest digests via GET /images/{id}/json.
// An image with no RepoDigests entry (built locally, never pushed) is
// reported as an empty slice and no error: it is legitimately digest-less.
// A request failure, a non-200, or an undecodable response is an error, and
// the caller keeps it on the Container instead of collapsing it into
// "no digests", so a failed lookup is never mistaken for a local build.
// Docker records one entry per manifest digest the image has ever been
// pulled under, so every entry is returned, not just the first: the same
// repository can legitimately appear twice under different digests after a
// retag.
func (c *Client) repoDigests(ctx context.Context, imageID string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/v1.44/images/"+imageID+"/json", nil)
	if err != nil {
		return nil, fmt.Errorf("local: building image inspect request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("local: inspecting image %s: %w", imageID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("local: inspecting image %s: docker returned %s", imageID, resp.Status)
	}

	var img apiImageInspect
	if err := json.NewDecoder(resp.Body).Decode(&img); err != nil {
		return nil, fmt.Errorf("local: decoding image inspect for %s: %w", imageID, err)
	}

	out := make([]string, 0, len(img.RepoDigests))
	for _, rd := range img.RepoDigests {
		if i := strings.LastIndex(rd, "@"); i >= 0 {
			out = append(out, rd[i+1:])
		}
	}
	return out, nil
}

// Index keys containers by "project/service" for lookup during report
// building, keeping every replica of a service. A service can have several
// running replicas whose images differ, so collapsing them into one entry --
// last one wins, in whatever order the daemon happened to answer -- would hide
// a stale deployment behind a fresh one. One-off containers are excluded: they
// are not replicas of a running service.
//
// Each key's slice is sorted, so a report built from the same containers is
// the same report whichever order Docker returned them in.
func Index(cs []Container) map[string][]Container {
	out := make(map[string][]Container, len(cs))
	for _, c := range cs {
		if c.OneOff {
			continue
		}
		key := c.Project + "/" + c.Service
		out[key] = append(out[key], c)
	}
	for key, replicas := range out {
		sort.Slice(replicas, func(i, j int) bool { return less(replicas[i], replicas[j]) })
		out[key] = replicas
	}
	return out
}

// less orders replicas by image reference, then by the digest set, so
// deterministic output does not depend on the daemon's ordering.
func less(a, b Container) bool {
	if a.Image != b.Image {
		return a.Image < b.Image
	}
	return strings.Join(a.RepoDigests, ",") < strings.Join(b.RepoDigests, ",")
}
