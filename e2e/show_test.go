package e2e

import (
	"fmt"
	"strings"
	"testing"
)

// A digest change is never "just a hash": the same source revision rebuilt is
// a different answer from a revision that moved.
func TestShowExplainsWhyADigestChanged(t *testing.T) {
	tests := []struct {
		name     string
		first    image
		second   image
		wantText string
	}{
		{
			// The same source, built again: a base image or dependency
			// refresh, not an upstream change.
			name:     "rebuilt from the same revision",
			first:    image{Revision: "abc123def456789", Created: on(1)},
			second:   image{Revision: "abc123def456789", Created: on(6)},
			wantText: "rebuilt from the same upstream revision (abc123de)",
		},
		{
			name:     "upstream revision moved",
			first:    image{Revision: "abc123def456789", Created: on(1)},
			second:   image{Revision: "987654fedcba321", Created: on(6)},
			wantText: "upstream revision moved: abc123de -> 987654fe",
		},
		{
			name:     "no revision label at all",
			first:    image{NoRevision: true, Created: on(1)},
			second:   image{NoRevision: true, Created: on(6)},
			wantText: "no revision label, so the cause cannot be determined without pulling",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			declared := e.push("app", "1.0.0", tt.first)
			moved := e.push("app", "1.0.0", tt.second)
			e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s@%s\n", e.ref("app", "1.0.0"), declared))

			got := e.run("show", "demo")
			assertCode(t, "show", got.code, 0, got)
			assertContains(t, "detail", got.stdout,
				"demo / api", "status:    digest-drift", "digest change",
				"declared: "+declared, "registry: "+moved, tt.wantText)
		})
	}
}

// The detail view is where a user reads what an update actually is, so the
// reference, the version, the candidate and the reason all have to be there.
func TestShowDescribesAnAvailableUpdate(t *testing.T) {
	e := newEnv(t)
	e.push("app", "1.0.0", image{})
	e.push("app", "2.0.0", image{})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0")))

	got := e.run("show", "demo")
	assertCode(t, "show", got.code, 0, got)
	assertContains(t, "detail", got.stdout,
		"demo / api", "reference: "+e.ref("app", "1.0.0"),
		"status:    update-available", "version:   1.0.0", "available: 2.0.0 (major)")
}

// Release notes are the payoff of the whole tool, so the range between the
// deployed version and the candidate is rendered, oldest first, and the
// deployed release itself is not repeated. A "v" prefix on the upstream tag
// must not hide anything.
func TestShowListsReleaseNotesBetweenTheDeployedAndAvailableVersions(t *testing.T) {
	e := newEnv(t)
	source := "https://github.com/e2e/acme"
	e.push("acme", "1.1.0", image{Version: "1.1.0", Source: source})
	e.push("acme", "1.2.0", image{Version: "1.2.0", Source: source})
	e.push("acme", "1.3.0", image{Version: "1.3.0", Source: source})
	e.gh.publish("e2e/acme",
		ghRelease{Tag: "v1.3.0", Body: "Third: faster startup."},
		ghRelease{Tag: "v1.2.0", Body: "Second: fixed a leak."},
		ghRelease{Tag: "v1.1.0", Body: "First: the deployed one."},
		ghRelease{Tag: "v1.0.0", Body: "Older than what is deployed."},
	)
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("acme", "1.1.0")))

	got := e.run("show", "demo")
	assertCode(t, "show", got.code, 0, got)
	assertContains(t, "detail", got.stdout, "release notes", "v1.2.0", "Second: fixed a leak.", "v1.3.0", "Third: faster startup.")
	assertOmits(t, "detail", got.stdout, "First: the deployed one.", "Older than what is deployed.")
	if strings.Index(got.stdout, "v1.2.0") > strings.Index(got.stdout, "v1.3.0") {
		t.Errorf("release notes are not oldest first:\n%s", got.stdout)
	}
}

// Without an answer, say why: silence reads as "nothing changed".
func TestShowSaysWhenNoNotesAreAvailable(t *testing.T) {
	e := newEnv(t)
	e.push("acme", "1.0.0", image{Version: "1.0.0"})
	e.push("acme", "1.1.0", image{Version: "1.1.0"})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("acme", "1.0.0")))

	got := e.run("show", "demo")
	assertCode(t, "show", got.code, 0, got)
	assertContains(t, "detail", got.stdout,
		"release notes", "no release notes available", "no source label, and no repo configured")
}

// A source label that is not GitHub, and no configuration, is the same dead
// end -- and a config override is the way out of it.
func TestShowFallsBackToAConfiguredReleaseRepository(t *testing.T) {
	e := newEnv(t)
	e.push("acme", "1.0.0", image{Version: "1.0.0", Source: "https://gitlab.com/e2e/acme"})
	e.push("acme", "1.1.0", image{Version: "1.1.0", Source: "https://gitlab.com/e2e/acme"})
	e.gh.publish("e2e/acme", ghRelease{Tag: "v1.1.0", Body: "From the configured repository."})
	e.config("[stacks.demo.images.acme]\nrepo = \"e2e/acme\"\n")
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("acme", "1.0.0")))

	got := e.run("show", "demo")
	assertCode(t, "show", got.code, 0, got)
	assertContains(t, "detail", got.stdout, "v1.1.0", "From the configured repository.")
}

// A row stackmon could not resolve must show the reason, not a blank line.
func TestShowSurfacesAnUnknownRow(t *testing.T) {
	e := newEnv(t)
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("ghost", "1.0.0")))

	got := e.run("show", "demo")
	assertCode(t, "show", got.code, 0, got)
	assertContains(t, "detail", got.stdout, "status:    unknown", "error:")
}
