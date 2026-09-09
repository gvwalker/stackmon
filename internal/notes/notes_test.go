package notes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const releasesJSON = `[
  {"tag_name":"v1.3.0","name":"1.3.0","body":"Third","html_url":"https://x/3","published_at":"2026-03-01T00:00:00Z"},
  {"tag_name":"v1.2.0","name":"1.2.0","body":"Second","html_url":"https://x/2","published_at":"2026-02-01T00:00:00Z"},
  {"tag_name":"v1.1.0","name":"1.1.0","body":"First","html_url":"https://x/1","published_at":"2026-01-01T00:00:00Z"}
]`

func fakeGitHub(t *testing.T, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	// go-github requires a trailing slash on the base URL.
	c, err := NewWithBaseURL("", srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBetweenReturnsIntermediateReleasesOldestFirst(t *testing.T) {
	c := fakeGitHub(t, releasesJSON)

	got, err := c.Between(context.Background(), "traefik/traefik", "v1.1.0", "v1.3.0")
	if err != nil {
		t.Fatalf("Between error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d releases, want 2 (v1.2.0 and v1.3.0)", len(got))
	}
	if got[0].Tag != "v1.2.0" {
		t.Errorf("got[0].Tag = %q, want v1.2.0 (oldest first)", got[0].Tag)
	}
	if got[1].Tag != "v1.3.0" {
		t.Errorf("got[1].Tag = %q, want v1.3.0", got[1].Tag)
	}
}

func TestBetweenExcludesTheCurrentRelease(t *testing.T) {
	c := fakeGitHub(t, releasesJSON)

	got, err := c.Between(context.Background(), "o/r", "v1.2.0", "v1.3.0")
	if err != nil {
		t.Fatalf("Between error: %v", err)
	}
	for _, r := range got {
		if r.Tag == "v1.2.0" {
			t.Error("Between must exclude the version already deployed")
		}
	}
}

func TestBetweenIgnoresReleasesBeyondTarget(t *testing.T) {
	c := fakeGitHub(t, releasesJSON)

	got, err := c.Between(context.Background(), "o/r", "v1.1.0", "v1.2.0")
	if err != nil {
		t.Fatalf("Between error: %v", err)
	}
	if len(got) != 1 || got[0].Tag != "v1.2.0" {
		t.Errorf("got %+v, want only v1.2.0", got)
	}
}

func TestBetweenToleratesTagPrefixMismatch(t *testing.T) {
	// The image tag may be "1.2.0" while the GitHub tag is "v1.2.0".
	c := fakeGitHub(t, releasesJSON)

	got, err := c.Between(context.Background(), "o/r", "1.1.0", "1.3.0")
	if err != nil {
		t.Fatalf("Between error: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d releases, want 2 despite the missing v prefix", len(got))
	}
}

func TestRepoFromSource(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://github.com/traefik/traefik", "traefik/traefik"},
		{"https://github.com/linuxserver/docker-sonarr", "linuxserver/docker-sonarr"},
		{"https://github.com/cloudflare/cloudflared.git", "cloudflare/cloudflared"},
		{"https://gitlab.com/foo/bar", ""},
		{"", ""},
		{"not a url", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := RepoFromSource(tt.in); got != tt.want {
				t.Errorf("RepoFromSource(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
