// Package local reads running-container state from the Docker Engine API over
// its unix socket. It is one of only three packages permitted to perform I/O
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
	"strings"
	"time"
)

// DefaultSocket is the conventional Docker socket path.
const DefaultSocket = "/var/run/docker.sock"

// Compose sets these labels on every container it starts.
const (
	labelProject = "com.docker.compose.project"
	labelService = "com.docker.compose.service"
)

// Container is one running container started by Compose.
type Container struct {
	Project string
	Service string
	// Image is the reference the container was started from.
	Image string
	// RepoDigest is the manifest digest of the image actually in use,
	// resolved from the daemon's ImageID via GET /images/{id}/json's
	// RepoDigests. This is a different namespace from Docker's own
	// ImageID, which hashes the local image config JSON, not the registry
	// manifest: comparing ImageID directly against a compose @sha256 pin
	// or a registry.Inspect result compares two unrelated hashes. Empty
	// when the image has no RepoDigests entry, e.g. built locally and
	// never pushed.
	RepoDigest string
}

// Prober is the surface Task 10 depends on, so that report building can be
// tested without a Docker socket.
type Prober interface {
	Containers(ctx context.Context) ([]Container, error)
	Available() bool
}

// Client queries the Docker Engine API.
type Client struct {
	socket string
	http   *http.Client
}

// New returns a Client using the default socket path.
func New() *Client { return NewWithSocket(DefaultSocket) }

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
	digests := map[string]string{}
	for _, r := range raw {
		project, service := r.Labels[labelProject], r.Labels[labelService]
		if project == "" || service == "" {
			continue
		}

		digest := ""
		if r.ImageID != "" {
			d, ok := digests[r.ImageID]
			if !ok {
				d = c.repoDigest(ctx, r.ImageID)
				digests[r.ImageID] = d
			}
			digest = d
		}

		out = append(out, Container{
			Project:    project,
			Service:    service,
			Image:      r.Image,
			RepoDigest: digest,
		})
	}
	return out, nil
}

// repoDigest resolves imageID's manifest digest via GET /images/{id}/json.
// The running-container signal is best-effort: a request failure or an
// image with no RepoDigests entry (built locally, never pushed) yields an
// empty string rather than failing the whole Containers call.
func (c *Client) repoDigest(ctx context.Context, imageID string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/v1.44/images/"+imageID+"/json", nil)
	if err != nil {
		return ""
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var img apiImageInspect
	if err := json.NewDecoder(resp.Body).Decode(&img); err != nil {
		return ""
	}

	for _, rd := range img.RepoDigests {
		if i := strings.LastIndex(rd, "@"); i >= 0 {
			return rd[i+1:]
		}
	}
	return ""
}

// Index keys containers by "project/service" for lookup during report
// building.
func Index(cs []Container) map[string]Container {
	out := make(map[string]Container, len(cs))
	for _, c := range cs {
		out[c.Project+"/"+c.Service] = c
	}
	return out
}
