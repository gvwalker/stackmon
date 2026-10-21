package e2e

import (
	"fmt"
	"strings"
	"testing"
)

// Exercise repository identity at the CLI boundary: real Docker Hub names,
// registry probes routed to local fixtures, reports, release notes and writes.
func TestImageOverrideAliases(t *testing.T) {
	for _, tc := range []struct{ name, key, ref, repo string }{
		{"official shorthand", "traefik", "docker.io/library/traefik", "library/traefik"},
		{"official canonical", "index.docker.io/library/traefik", "traefik", "library/traefik"},
		{"official library", "library/traefik", "docker.io/traefik", "library/traefik"},
		{"non official", "acme/traefik", "docker.io/acme/traefik", "acme/traefik"},
		{"explicit registry", "ghcr.io/acme/traefik", "ghcr.io/acme/traefik", "acme/traefik"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.set("STACKMON_E2E_REGISTRY", e.host)
			e.push(tc.repo, "1.0.0", image{})
			e.push(tc.repo, "1.1.0", image{})
			e.push(tc.repo, "2.0.0", image{})
			e.gh.publish("e2e/notes", ghRelease{Tag: "1.1.0", Body: "Configured release notes."})
			e.config(fmt.Sprintf("[stacks.demo.images.%q]\nrepo = \"e2e/notes\"\nconstraint = \"1.*\"\n", tc.key))
			path := e.enrollStack("demo", fmt.Sprintf("services:\n  app:\n    image: %s:1.0.0\n", tc.ref))
			e.hits.reset()
			row := e.checkJSON().image(t, "demo", "app")
			assertEq(t, "candidate", row.Candidate, "1.1.0")
			assertEq(t, "notes repository", row.NotesRepo, "e2e/notes")
			assertEq(t, "tag list requests", e.hits.count("GET /v2/"+tc.repo+"/tags/list"), 1)
			e.note("override %q: candidate=%s notes=%s; tag lists=1", tc.key, row.Candidate, row.NotesRepo)
			shown := e.run("show", "demo")
			assertCode(t, "show", shown.code, 0, shown)
			assertContains(t, "show", shown.stdout, "available: 1.1.0", "Configured release notes.")
			bumped := e.run("bump", "demo")
			assertCode(t, "bump", bumped.code, 0, bumped)
			assertContains(t, "compose", e.read(path), tc.ref+":1.1.0")
			e.note("compose after bump:\n%s", e.read(path))
		})
	}
}

func TestImageOverrideRegistrySeparation(t *testing.T) {
	e := newEnv(t)
	e.set("STACKMON_E2E_REGISTRY", e.host)
	e.push("library/traefik", "pg15", image{})
	e.push("library/traefik", "pg18", image{})
	e.push("acme/traefik", "pg15", image{})
	e.push("acme/traefik", "pg18", image{})
	e.config(`[stacks.demo.images.traefik]
constraint = "pg18*"
repo = "e2e/official"
[stacks.demo.images."ghcr.io/library/traefik"]
constraint = "pg99*"
repo = "e2e/private"
[stacks.demo.images."acme/traefik"]
constraint = "pg18*"
repo = "e2e/acme"
`)
	e.enrollStack("demo", "services:\n  official:\n    image: traefik:pg15\n  private:\n    image: ghcr.io/library/traefik:pg15\n  named:\n    image: acme/traefik:pg15\n  other:\n    image: other.io/acme/traefik:pg15\n")
	rep := e.checkJSON()
	for _, tc := range []struct{ service, candidate, notes string }{
		{"official", "pg18", "e2e/official"}, {"private", "", "e2e/private"},
		{"named", "pg18", "e2e/acme"}, {"other", "", ""},
	} {
		row := rep.image(t, "demo", tc.service)
		assertEq(t, tc.service+" candidate", row.Candidate, tc.candidate)
		assertEq(t, tc.service+" notes", row.NotesRepo, tc.notes)
		e.note("%s: candidate=%q notes=%q", tc.service, row.Candidate, row.NotesRepo)
	}
}

func TestImageOverrideDuplicateAliases(t *testing.T) {
	for _, field := range []string{"repo", "constraint", "identical"} {
		t.Run(field, func(t *testing.T) {
			e := newEnv(t)
			second := "repo = \"e2e/notes\"\nconstraint = \"pg18*\"\n"
			if field == "repo" {
				second = strings.ReplaceAll(second, "e2e/notes", "e2e/other")
			}
			if field == "constraint" {
				second = strings.ReplaceAll(second, "pg18*", "pg19*")
			}
			e.config("[stacks.demo.images.traefik]\nrepo = \"e2e/notes\"\nconstraint = \"pg18*\"\n[stacks.demo.images.\"docker.io/library/traefik\"]\n" + second)
			if field == "identical" {
				e.set("STACKMON_E2E_REGISTRY", e.host)
				e.push("library/traefik", "pg15", image{})
				e.push("library/traefik", "pg18", image{})
				e.enrollStack("demo", "services:\n  app:\n    image: traefik:pg15\n")
				row := e.checkJSON().image(t, "demo", "app")
				assertEq(t, "candidate", row.Candidate, "pg18")
				assertEq(t, "notes", row.NotesRepo, "e2e/notes")
				e.note("identical aliases coalesced: candidate=%s notes=%s", row.Candidate, row.NotesRepo)
			} else {
				e.hits.reset()
				var previous string
				for range 3 {
					r := e.exec(devBinary, "check")
					assertCode(t, "conflict", r.code, 1, r)
					assertContains(t, "conflict", r.stderr, "demo", "traefik", "docker.io/library/traefik", "conflict")
					if previous != "" {
						assertEq(t, "deterministic error", r.stderr, previous)
					}
					previous = r.stderr
				}
				e.note("conflict (%s): %s", field, e.normalize(previous))
				assertEq(t, "no registry traffic", e.hits.count(""), 0)
			}
		})
	}
}
