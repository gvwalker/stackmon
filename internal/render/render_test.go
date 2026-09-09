package render

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gvwalker/stackmon/internal/notes"
	"github.com/gvwalker/stackmon/internal/report"
)

func sampleReport() report.Report {
	return report.Report{
		Generated:       time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC),
		DockerAvailable: true,
		Images: []report.Image{
			{Stack: "adguard", Service: "adguard", Version: "v0.107.79",
				Candidate: "v0.107.80", KindName: "patch", Status: report.StatusUpdateAvailable},
			{Stack: "media", Service: "sonarr", Version: "latest",
				Status: report.StatusStaleDeployment},
			{Stack: "traefik", Service: "traefik", Version: "v3.7.10",
				Status: report.StatusCurrent},
			{Stack: "broken", Service: "app", Status: report.StatusUnknown,
				Err: "registry unreachable"},
		},
	}
}

func TestTableMatchesGolden(t *testing.T) {
	var buf bytes.Buffer
	if err := Table(&buf, sampleReport()); err != nil {
		t.Fatalf("Table error: %v", err)
	}

	golden := "testdata/table.golden"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if buf.String() != string(want) {
		t.Errorf("Table output mismatch.\ngot:\n%s\nwant:\n%s", buf.String(), want)
	}
}

// A file-only report must say so, or a partial answer reads as a complete one.
func TestTableWarnsWhenDockerUnavailable(t *testing.T) {
	r := sampleReport()
	r.DockerAvailable = false

	var buf bytes.Buffer
	if err := Table(&buf, r); err != nil {
		t.Fatalf("Table error: %v", err)
	}
	if !strings.Contains(buf.String(), "docker socket unavailable") {
		t.Errorf("output does not warn about the missing docker signal:\n%s", buf.String())
	}
}

func TestTableShowsErrorReason(t *testing.T) {
	var buf bytes.Buffer
	if err := Table(&buf, sampleReport()); err != nil {
		t.Fatalf("Table error: %v", err)
	}
	if !strings.Contains(buf.String(), "registry unreachable") {
		t.Errorf("unknown row does not carry its reason:\n%s", buf.String())
	}
}

func TestDetailShowsDigestChangeExplanation(t *testing.T) {
	img := report.Image{
		Stack: "traefik", Service: "traefik", Version: "v3.7.10",
		Status:           report.StatusDigestDrift,
		DeclaredDigest:   "sha256:aaaa",
		RegistryDigest:   "sha256:bbbb",
		Created:          time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		RegistryCreated:  time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC),
		Revision:         "abc123",
		RegistryRevision: "abc123",
	}

	var buf bytes.Buffer
	if err := Detail(&buf, img, nil); err != nil {
		t.Fatalf("Detail error: %v", err)
	}
	out := buf.String()
	// Same revision, later build date: a rebuild, not new source.
	if !strings.Contains(out, "same upstream revision") {
		t.Errorf("detail does not explain a same-revision rebuild:\n%s", out)
	}
	if !strings.Contains(out, "sha256:bbbb") {
		t.Errorf("detail omits the new digest:\n%s", out)
	}
}

func TestDetailRendersReleaseNotes(t *testing.T) {
	img := report.Image{
		Stack: "hindsight", Service: "app", Version: "0.9.1",
		Candidate: "1.0.0", KindName: "major", Status: report.StatusUpdateAvailable,
	}
	rels := []notes.Release{
		{Tag: "0.9.2", Name: "0.9.2", Body: "Fixed a leak."},
		{Tag: "1.0.0", Name: "1.0.0", Body: "Breaking: renamed the config key."},
	}

	var buf bytes.Buffer
	if err := Detail(&buf, img, rels); err != nil {
		t.Fatalf("Detail error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"0.9.2", "Fixed a leak.", "1.0.0", "Breaking: renamed the config key."} {
		if !strings.Contains(out, want) {
			t.Errorf("detail omits %q:\n%s", want, out)
		}
	}
}

func TestDetailStatesWhenNoNotesAvailable(t *testing.T) {
	img := report.Image{
		Stack: "db", Service: "postgres", Version: "18-alpine",
		Candidate: "19-alpine", Status: report.StatusUpdateAvailable,
	}

	var buf bytes.Buffer
	if err := Detail(&buf, img, nil); err != nil {
		t.Fatalf("Detail error: %v", err)
	}
	if !strings.Contains(buf.String(), "no release notes available") {
		t.Errorf("detail should state that notes are unavailable:\n%s", buf.String())
	}
}
