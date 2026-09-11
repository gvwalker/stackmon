package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

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

const latestJSON = `{"tag_name":"v1.3.0","body":"notes","html_url":"https://x/3"}`

func TestLatestReportsNewerRelease(t *testing.T) {
	c := fakeGitHub(t, latestJSON)

	rel, newer, err := c.Latest(context.Background(), "v1.2.0")
	if err != nil {
		t.Fatalf("Latest error: %v", err)
	}
	if !newer {
		t.Error("newer = false, want true (v1.3.0 > v1.2.0)")
	}
	if rel.Tag != "v1.3.0" {
		t.Errorf("Tag = %q, want v1.3.0", rel.Tag)
	}
}

func TestLatestReportsUpToDate(t *testing.T) {
	c := fakeGitHub(t, latestJSON)

	_, newer, err := c.Latest(context.Background(), "v1.3.0")
	if err != nil {
		t.Fatalf("Latest error: %v", err)
	}
	if newer {
		t.Error("newer = true, want false when already on the latest tag")
	}
}

func TestLatestNeverClaimsNewerForUnparseableCurrent(t *testing.T) {
	c := fakeGitHub(t, latestJSON)

	_, newer, err := c.Latest(context.Background(), "dev")
	if err != nil {
		t.Fatalf("Latest error: %v", err)
	}
	if newer {
		t.Error("newer = true, want false for an unparseable current version like \"dev\"")
	}
}

func TestParseChecksums(t *testing.T) {
	text := "abc123  stackmon-linux-amd64\ndef456  stackmon-darwin-arm64\n"
	got := parseChecksums([]byte(text))
	if got["stackmon-linux-amd64"] != "abc123" {
		t.Errorf("stackmon-linux-amd64 = %q, want abc123", got["stackmon-linux-amd64"])
	}
	if got["stackmon-darwin-arm64"] != "def456" {
		t.Errorf("stackmon-darwin-arm64 = %q, want def456", got["stackmon-darwin-arm64"])
	}
}

func TestInstallReplacesFileAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stackmon")
	if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := install(path, []byte("new")); err != nil {
		t.Fatalf("install error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("content = %q, want new", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Error("installed binary is not executable")
	}
}
