package local

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// fakeDocker serves body at /containers/json over a unix socket.
func fakeDocker(t *testing.T, status int, body string) string {
	t.Helper()
	// Socket paths are length-limited, so keep this short.
	dir, err := os.MkdirTemp("", "sm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

const twoContainers = `[
  {"Id":"aaa","Names":["/radarr"],"Image":"lscr.io/linuxserver/radarr:latest",
   "ImageID":"sha256:bc7263170111bf1f874398dd59837800721f9c5c2d2ee34144abfc0bf6809b85",
   "Labels":{"com.docker.compose.project":"mediaserver","com.docker.compose.service":"radarr"}},
  {"Id":"bbb","Names":["/traefik"],"Image":"traefik",
   "ImageID":"sha256:9c3b91d5fb7770853ca5c1124a23c34bf2d9b47ffaebeab2614cbaf410dcb2ac",
   "Labels":{"com.docker.compose.project":"traefik","com.docker.compose.service":"traefik"}}
]`

func TestContainersReadsComposeLabels(t *testing.T) {
	sock := fakeDocker(t, http.StatusOK, twoContainers)

	got, err := NewWithSocket(sock).Containers(context.Background())
	if err != nil {
		t.Fatalf("Containers error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Containers = %d, want 2", len(got))
	}
	if got[0].Project != "mediaserver" || got[0].Service != "radarr" {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[0].ImageID != "sha256:bc7263170111bf1f874398dd59837800721f9c5c2d2ee34144abfc0bf6809b85" {
		t.Errorf("ImageID = %q", got[0].ImageID)
	}
}

// Containers not started by Compose carry no project label and must not
// collide in the index.
func TestContainersSkipsNonComposeContainers(t *testing.T) {
	sock := fakeDocker(t, http.StatusOK,
		`[{"Id":"ccc","Names":["/manual"],"Image":"nginx","ImageID":"sha256:abc","Labels":{}}]`)

	got, err := NewWithSocket(sock).Containers(context.Background())
	if err != nil {
		t.Fatalf("Containers error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Containers = %+v, want none (no compose labels)", got)
	}
}

func TestIndexKeysByProjectAndService(t *testing.T) {
	idx := Index([]Container{
		{Project: "mediaserver", Service: "radarr", ImageID: "sha256:aaa"},
		{Project: "traefik", Service: "traefik", ImageID: "sha256:bbb"},
	})
	got, ok := idx["mediaserver/radarr"]
	if !ok {
		t.Fatal("index missing mediaserver/radarr")
	}
	if got.ImageID != "sha256:aaa" {
		t.Errorf("ImageID = %q", got.ImageID)
	}
}

func TestContainersErrorsOnDaemonFailure(t *testing.T) {
	sock := fakeDocker(t, http.StatusInternalServerError, `{"message":"boom"}`)

	if _, err := NewWithSocket(sock).Containers(context.Background()); err == nil {
		t.Fatal("Containers on 500 = nil error, want error")
	}
}

func TestAvailableFalseWhenSocketAbsent(t *testing.T) {
	c := NewWithSocket(filepath.Join(t.TempDir(), "absent.sock"))
	if c.Available() {
		t.Error("Available() = true for an absent socket")
	}
}

func TestAvailableTrueWhenSocketPresent(t *testing.T) {
	if !NewWithSocket(fakeDocker(t, http.StatusOK, "[]")).Available() {
		t.Error("Available() = false for a present socket")
	}
}
