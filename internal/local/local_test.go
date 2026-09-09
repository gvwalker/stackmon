package local

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDocker serves /containers/json plus /images/{id}/json over a unix
// socket, so Containers can resolve running images to their repo digest
// without a real daemon. repoDigests is keyed by the Docker ImageID (the
// local config digest); each value is the raw RepoDigests list Docker would
// report for that image. calls, if non-nil, counts /images/{id}/json
// requests.
func fakeDocker(t *testing.T, status int, containersBody string, repoDigests map[string][]string) string {
	t.Helper()
	return fakeDockerCounting(t, status, containersBody, repoDigests, nil)
}

func fakeDockerCounting(t *testing.T, status int, containersBody string, repoDigests map[string][]string, calls *int) string {
	t.Helper()
	// Socket paths are length-limited, so keep this short.
	dir := t.TempDir()

	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1.44/containers/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(containersBody))
	})
	mux.HandleFunc("/v1.44/images/", func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			*calls++
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1.44/images/"), "/json")
		digests, ok := repoDigests[id]
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"no such image"}`))
			return
		}
		body, _ := json.Marshal(struct {
			RepoDigests []string
		}{RepoDigests: digests})
		_, _ = w.Write(body)
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// Docker's ImageID hashes the local image config JSON, not the registry
// manifest, so these two are deliberately different values from the
// RepoDigests entries below: using one for the other is exactly the bug
// this package must not reintroduce.
const twoContainers = `[
  {"Id":"aaa","Names":["/radarr"],"Image":"lscr.io/linuxserver/radarr:latest",
   "ImageID":"sha256:1111111111111111111111111111111111111111111111111111111111111111",
   "Labels":{"com.docker.compose.project":"mediaserver","com.docker.compose.service":"radarr"}},
  {"Id":"bbb","Names":["/traefik"],"Image":"traefik",
   "ImageID":"sha256:2222222222222222222222222222222222222222222222222222222222222222",
   "Labels":{"com.docker.compose.project":"traefik","com.docker.compose.service":"traefik"}}
]`

var twoContainersRepoDigests = map[string][]string{
	"sha256:1111111111111111111111111111111111111111111111111111111111111111": {
		"lscr.io/linuxserver/radarr@sha256:bc7263170111bf1f874398dd59837800721f9c5c2d2ee34144abfc0bf6809b85",
	},
	"sha256:2222222222222222222222222222222222222222222222222222222222222222": {
		"traefik@sha256:9c3b91d5fb7770853ca5c1124a23c34bf2d9b47ffaebeab2614cbaf410dcb2ac",
	},
}

func TestContainersReadsComposeLabels(t *testing.T) {
	sock := fakeDocker(t, http.StatusOK, twoContainers, twoContainersRepoDigests)

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
	if len(got[0].RepoDigests) != 1 || got[0].RepoDigests[0] != "sha256:bc7263170111bf1f874398dd59837800721f9c5c2d2ee34144abfc0bf6809b85" {
		t.Errorf("RepoDigests = %v, want [sha256:bc7263170111bf1f874398dd59837800721f9c5c2d2ee34144abfc0bf6809b85]", got[0].RepoDigests)
	}
}

// Containers not started by Compose carry no project label and must not
// collide in the index.
func TestContainersSkipsNonComposeContainers(t *testing.T) {
	sock := fakeDocker(t, http.StatusOK,
		`[{"Id":"ccc","Names":["/manual"],"Image":"nginx","ImageID":"sha256:abc","Labels":{}}]`,
		nil)

	got, err := NewWithSocket(sock).Containers(context.Background())
	if err != nil {
		t.Fatalf("Containers error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Containers = %+v, want none (no compose labels)", got)
	}
}

// The whole point: a config digest (ImageID) and manifest digests
// (RepoDigests) are unrelated hashes, so Containers must never hand back
// the former under the latter's name.
func TestContainersResolvesRepoDigestNotRawImageID(t *testing.T) {
	sock := fakeDocker(t, http.StatusOK, twoContainers, twoContainersRepoDigests)

	got, err := NewWithSocket(sock).Containers(context.Background())
	if err != nil {
		t.Fatalf("Containers error: %v", err)
	}
	for _, c := range got {
		if len(c.RepoDigests) == 0 {
			t.Errorf("%s/%s: RepoDigests empty, want a resolved manifest digest", c.Project, c.Service)
			continue
		}
		for _, d := range c.RepoDigests {
			if d == "sha256:1111111111111111111111111111111111111111111111111111111111111111" ||
				d == "sha256:2222222222222222222222222222222222222222222222222222222222222222" {
				t.Errorf("%s/%s: RepoDigests contains %q, want the manifest digest, not the raw ImageID", c.Project, c.Service, d)
			}
		}
	}
}

// A locally built image that was never pushed has no RepoDigests entry.
// Inventing a comparison for it would be worse than reporting nothing.
func TestContainersLeavesRepoDigestEmptyWhenImageHasNoRepoDigests(t *testing.T) {
	body := `[{"Id":"ddd","Names":["/homebrew"],"Image":"homebrew:local",
   "ImageID":"sha256:3333333333333333333333333333333333333333333333333333333333333333",
   "Labels":{"com.docker.compose.project":"lab","com.docker.compose.service":"homebrew"}}]`
	sock := fakeDocker(t, http.StatusOK, body, map[string][]string{
		"sha256:3333333333333333333333333333333333333333333333333333333333333333": nil,
	})

	got, err := NewWithSocket(sock).Containers(context.Background())
	if err != nil {
		t.Fatalf("Containers error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Containers = %d, want 1", len(got))
	}
	if len(got[0].RepoDigests) != 0 {
		t.Errorf("RepoDigests = %v, want empty for an image with no RepoDigests", got[0].RepoDigests)
	}
}

// N containers sharing one image must cost one extra request, not N.
func TestContainersCachesRepoDigestLookupPerImageID(t *testing.T) {
	const sharedID = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
	body := `[
  {"Id":"a1","Names":["/a"],"Image":"redis:8.2","ImageID":"` + sharedID + `",
   "Labels":{"com.docker.compose.project":"cache","com.docker.compose.service":"a"}},
  {"Id":"a2","Names":["/b"],"Image":"redis:8.2","ImageID":"` + sharedID + `",
   "Labels":{"com.docker.compose.project":"cache","com.docker.compose.service":"b"}}
]`
	var calls int
	sock := fakeDockerCounting(t, http.StatusOK, body, map[string][]string{
		sharedID: {"redis@sha256:aaaabbbbccccddddeeeeffffaaaabbbbccccddddeeeeffffaaaabbbbccccdddd"},
	}, &calls)

	got, err := NewWithSocket(sock).Containers(context.Background())
	if err != nil {
		t.Fatalf("Containers error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Containers = %d, want 2", len(got))
	}
	if calls != 1 {
		t.Errorf("images/json called %d times for 2 containers sharing an image, want 1", calls)
	}
}

func TestContainersErrorsOnDaemonFailure(t *testing.T) {
	sock := fakeDocker(t, http.StatusInternalServerError, `{"message":"boom"}`, nil)

	if _, err := NewWithSocket(sock).Containers(context.Background()); err == nil {
		t.Fatal("Containers on 500 = nil error, want error")
	}
}

func TestIndexKeysByProjectAndService(t *testing.T) {
	idx := Index([]Container{
		{Project: "mediaserver", Service: "radarr", RepoDigests: []string{"sha256:aaa"}},
		{Project: "traefik", Service: "traefik", RepoDigests: []string{"sha256:bbb"}},
	})
	got, ok := idx["mediaserver/radarr"]
	if !ok {
		t.Fatal("index missing mediaserver/radarr")
	}
	if len(got.RepoDigests) != 1 || got.RepoDigests[0] != "sha256:aaa" {
		t.Errorf("RepoDigests = %v", got.RepoDigests)
	}
}

// Docker records one RepoDigests entry per manifest digest an image has
// ever been pulled under; the same repository can legitimately appear
// twice after a retag. Containers must preserve every entry, not just the
// first, or a declared digest matching the second entry reads as
// undeployed.
func TestContainersPreservesEveryRepoDigestEntry(t *testing.T) {
	const imageID = "sha256:5555555555555555555555555555555555555555555555555555555555555555"
	body := `[{"Id":"eee","Names":["/postgres"],"Image":"postgres:18-alpine",
   "ImageID":"` + imageID + `",
   "Labels":{"com.docker.compose.project":"db","com.docker.compose.service":"postgres"}}]`
	sock := fakeDocker(t, http.StatusOK, body, map[string][]string{
		imageID: {
			"postgres@sha256:a1d02e4bfeb5da3bdb2a3372ab0e1e8e4dc4bec8d1b3e3e6b02c4e2fca6bd2ac",
			"postgres@sha256:d3e1620bfeb5da3bdb2a3372ab0e1e8e4dc4bec8d1b3e3e6b02c4e2fca6bd2ac",
		},
	})

	got, err := NewWithSocket(sock).Containers(context.Background())
	if err != nil {
		t.Fatalf("Containers error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Containers = %d, want 1", len(got))
	}
	want := []string{
		"sha256:a1d02e4bfeb5da3bdb2a3372ab0e1e8e4dc4bec8d1b3e3e6b02c4e2fca6bd2ac",
		"sha256:d3e1620bfeb5da3bdb2a3372ab0e1e8e4dc4bec8d1b3e3e6b02c4e2fca6bd2ac",
	}
	if len(got[0].RepoDigests) != 2 || got[0].RepoDigests[0] != want[0] || got[0].RepoDigests[1] != want[1] {
		t.Errorf("RepoDigests = %v, want %v (every RepoDigests entry, not just the first)", got[0].RepoDigests, want)
	}
}

func TestAvailableFalseWhenSocketAbsent(t *testing.T) {
	c := NewWithSocket(filepath.Join(t.TempDir(), "absent.sock"))
	if c.Available() {
		t.Error("Available() = true for an absent socket")
	}
}

func TestAvailableTrueWhenSocketPresent(t *testing.T) {
	if !NewWithSocket(fakeDocker(t, http.StatusOK, "[]", nil)).Available() {
		t.Error("Available() = false for a present socket")
	}
}
