//go:build stackmon_e2e

package registry

import (
	"net/http"
	"os"

	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Route registry traffic to the scenario's local fixture without changing the
// image identity under test. This hook is absent from shipped binaries.
func init() {
	if host := os.Getenv("STACKMON_E2E_REGISTRY"); host != "" {
		remote.DefaultTransport = fixtureTransport{host: host, base: remote.DefaultTransport}
	}
}

type fixtureTransport struct {
	host string
	base http.RoundTripper
}

func (t fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = t.host
	clone.Host = t.host
	return t.base.RoundTrip(clone)
}
