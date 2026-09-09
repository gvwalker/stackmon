package compose

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// yaml.Node.Column is a 1-indexed rune count, not a byte count. A multi-byte
// UTF-8 character earlier on the same line must not throw off the computed
// byte offset of a later image value.
func TestLoadRecordsOffsetWithMultibyteUTF8OnLine(t *testing.T) {
	st, err := Load(context.Background(), "unicode", "testdata/unicode", "docker-compose.yml")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join("testdata/unicode", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Services) != 1 {
		t.Fatalf("Services = %d, want 1", len(st.Services))
	}
	svc := st.Services[0]
	got := string(data[svc.Offset : svc.Offset+svc.Length])
	if got != svc.Ref.Raw {
		t.Errorf("bytes at offset = %q, want %q", got, svc.Ref.Raw)
	}
}

// A quoted image value is idiomatic YAML and near-mandatory for some
// interpolated refs. yaml.v3 reports a quoted scalar's Column as pointing at
// the opening quote, not the first content character, so the offset
// self-check must account for the quote characters or it mismatches and
// aborts the whole stack.
func TestLoadAcceptsSingleQuotedImageValue(t *testing.T) {
	dir := t.TempDir()
	body := "services:\n  web:\n    image: 'redis:8.2'\n"
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := Load(context.Background(), "cache", dir, "compose.yaml")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(st.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none", st.Warnings)
	}
	if len(st.Services) != 1 || st.Services[0].Ref.Resolved != "redis:8.2" {
		t.Fatalf("Services = %+v, want one service resolving to redis:8.2", st.Services)
	}

	// The stored range must bracket the inner text, quotes excluded, so
	// bump can rewrite the value while leaving the quotes in place.
	data, err := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	svc := st.Services[0]
	got := string(data[svc.Offset : svc.Offset+svc.Length])
	if got != "redis:8.2" {
		t.Errorf("bytes at offset = %q, want the unquoted image text", got)
	}
}

func TestLoadAcceptsDoubleQuotedImageValue(t *testing.T) {
	dir := t.TempDir()
	body := "services:\n  web:\n    image: \"redis:8.2\"\n"
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := Load(context.Background(), "cache", dir, "compose.yaml")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(st.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none", st.Warnings)
	}
	if len(st.Services) != 1 || st.Services[0].Ref.Resolved != "redis:8.2" {
		t.Fatalf("Services = %+v, want one service resolving to redis:8.2", st.Services)
	}

	data, err := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	svc := st.Services[0]
	got := string(data[svc.Offset : svc.Offset+svc.Length])
	if got != "redis:8.2" {
		t.Errorf("bytes at offset = %q, want the unquoted image text", got)
	}
}

// A stack name with an uppercase letter, dot, or space is not itself
// invalid: compose-go rejects it only when SetProjectName is imperatively
// set with the raw name, so Load must normalise it first, matching what
// Docker Compose itself does.
func TestLoadNormalisesNonLowercaseProjectName(t *testing.T) {
	if _, err := Load(context.Background(), "Nextcloud", "testdata/traefik", "docker-compose.yml"); err != nil {
		t.Fatalf("Load error: %v", err)
	}
}

// A service whose image value cannot be verified must not erase its
// siblings: Load skips it with a recorded warning and keeps everything else
// that did parse correctly.
func TestLoadSkipsUnverifiableServiceButKeepsSiblings(t *testing.T) {
	dir := t.TempDir()
	// A literal block scalar is a real YAML style Load does not special-case
	// (only plain, single- and double-quoted scalars are): its raw
	// representation includes the block indicator and indentation, so the
	// decoded value's bytes never equal the span at the recorded position.
	body := "services:\n  web:\n    image: |-\n      redis:8.2\n  cache:\n    image: redis:8.2\n"
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := Load(context.Background(), "lab", dir, "compose.yaml")
	if err != nil {
		t.Fatalf("Load error: %v, want nil (one bad service must not fail the whole stack)", err)
	}
	if len(st.Services) != 1 || st.Services[0].Name != "cache" {
		t.Fatalf("Services = %+v, want only cache", st.Services)
	}
	if len(st.Warnings) != 1 {
		t.Fatalf("Warnings = %v, want exactly one", st.Warnings)
	}
	if !strings.Contains(st.Warnings[0].Error(), "web") {
		t.Errorf("warning should name the skipped service, got: %v", st.Warnings[0])
	}
}
