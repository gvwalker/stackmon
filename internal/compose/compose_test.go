package compose

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gvwalker/stackmon/internal/imageref"
)

func TestLoadReadsDigestOnlyPin(t *testing.T) {
	st, err := Load(context.Background(), "traefik", "testdata/traefik", "docker-compose.yml")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(st.Services) != 1 {
		t.Fatalf("Services = %d, want 1", len(st.Services))
	}
	svc := st.Services[0]
	if svc.Name != "traefik" {
		t.Errorf("Name = %q, want traefik", svc.Name)
	}
	if svc.Ref.Shape != imageref.ShapeDigestOnly {
		t.Errorf("Shape = %v, want digest-only", svc.Ref.Shape)
	}
	if svc.Ref.Repository != "library/traefik" {
		t.Errorf("Repository = %q", svc.Ref.Repository)
	}
}

func TestLoadResolvesInterpolationWithDefault(t *testing.T) {
	st, err := Load(context.Background(), "karakeep", "testdata/karakeep", "docker-compose.yml")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	var web Service
	for _, s := range st.Services {
		if s.Name == "web" {
			web = s
		}
	}
	if web.Name == "" {
		t.Fatal("no service named web")
	}
	if web.Ref.Tag != "release" {
		t.Errorf("Tag = %q, want release (the ${VAR:-default})", web.Ref.Tag)
	}
	if !web.Ref.Interpolated {
		t.Error("Interpolated = false, want true")
	}
	if want := "ghcr.io/karakeep-app/karakeep:${KARAKEEP_VERSION:-release}"; web.Ref.Raw != want {
		t.Errorf("Raw = %q, want %q", web.Ref.Raw, want)
	}
}

// The offsets are what bump rewrites, so they must delimit exactly the image
// value and nothing else.
func TestLoadRecordsByteOffsetOfImageValue(t *testing.T) {
	st, err := Load(context.Background(), "traefik", "testdata/traefik", "docker-compose.yml")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join("testdata/traefik", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	svc := st.Services[0]
	got := string(data[svc.Offset : svc.Offset+svc.Length])
	if got != svc.Ref.Raw {
		t.Errorf("bytes at offset = %q, want %q", got, svc.Ref.Raw)
	}
}

func TestLoadRecordsOffsetsForEveryService(t *testing.T) {
	st, err := Load(context.Background(), "karakeep", "testdata/karakeep", "docker-compose.yml")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join("testdata/karakeep", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Services) != 2 {
		t.Fatalf("Services = %d, want 2", len(st.Services))
	}
	for _, svc := range st.Services {
		got := string(data[svc.Offset : svc.Offset+svc.Length])
		if got != svc.Ref.Raw {
			t.Errorf("%s: bytes at offset = %q, want %q", svc.Name, got, svc.Ref.Raw)
		}
	}
}

func TestLoadMissingFileErrors(t *testing.T) {
	if _, err := Load(context.Background(), "ghost", t.TempDir(), "compose.yaml"); err == nil {
		t.Fatal("Load of missing file = nil error, want error")
	}
}
