package local

import (
	"path/filepath"
	"sort"
	"strings"
)

// Identity is the Docker Compose project an enrolled stack turned out to be.
// The enrolled stack name is a display name and is not evidence of anything:
// Compose lets the project name be set independently of the directory
// (`name:` in the file, `docker compose -p`, `COMPOSE_PROJECT_NAME`), so the
// only trustworthy link between a stack and a running project is the path
// Compose recorded on the containers it started.
type Identity struct {
	// Project is the Compose project name the stack was matched to, empty
	// when no project runs this stack's compose file.
	Project string
	// Bound is true when Project came from an explicit enrollment binding
	// rather than from matching paths.
	Bound bool
	// Resolved is false when no project could be established: either the
	// stack's paths match more than one running project, or no running
	// project carries the labels that would identify one. An unresolved
	// identity is not a confirmed absence, so it must never be reported as
	// "not running".
	Resolved bool
	// Reason names why identity is unresolved, for the caller's diagnostic.
	// Empty when the identity is resolved, and when nothing runs this
	// stack's compose file: that is absence, not doubt.
	Reason string
	// Candidates lists the project names that matched, or every project the
	// daemon reported when nothing could be verified. Sorted.
	Candidates []string
}

// Reasons an identity could not be established.
const (
	// ReasonAmbiguous means the stack's paths match several running projects.
	ReasonAmbiguous = "ambiguous"
	// ReasonUnverifiable means no running project carries the path labels
	// identity would have to come from.
	ReasonUnverifiable = "unverifiable"
)

// ResolveProject establishes which Compose project runs the compose file at
// file, in directory dir.
//
// An explicit binding is taken at its word: it is what the user asserted, and
// silently switching to a different project because that one has no containers
// right now would report on a stack nobody asked about. A bound project that
// has nothing running resolves to a project with no containers, which the
// caller reports as "not running" -- a fact, not a guess.
//
// Without a binding, a project matches when Compose recorded this stack's
// compose file among its config files, or this stack's directory as its
// working directory. Paths are compared as paths: a substring match would let
// /srv/cache match /srv/cache-backup, which is a different stack. Several
// matching projects are ambiguous, never a reason to take the first.
func ResolveProject(cs []Container, boundProject, dir, file string) Identity {
	if boundProject != "" {
		return Identity{Project: boundProject, Bound: true, Resolved: true}
	}

	wantDir, wantFile := "", ""
	if dir != "" {
		wantDir = filepath.Clean(dir)
	}
	if file != "" {
		wantFile = filepath.Clean(file)
	}

	var matches []string
	seen := map[string]bool{}
	labelled := 0
	for _, c := range cs {
		if c.Project == "" || seen[c.Project] {
			continue
		}
		seen[c.Project] = true
		if !identifiable(c) {
			continue
		}
		labelled++
		if c.servesPath(wantDir, wantFile) {
			matches = append(matches, c.Project)
		}
	}
	sort.Strings(matches)

	switch {
	case len(matches) == 1:
		return Identity{Project: matches[0], Resolved: true}
	case len(matches) > 1:
		return Identity{Reason: ReasonAmbiguous, Candidates: matches}
	case labelled == 0 && len(seen) > 0:
		// Projects are running, but none of them says which directory it was
		// started from, so there is nothing to match this stack against.
		// Guessing from the display name here is exactly the bug this
		// replaces, so the caller is told to bind the project instead.
		return Identity{Reason: ReasonUnverifiable, Candidates: sortedKeys(seen)}
	default:
		// The daemon lists every running container, and a container running
		// this stack's compose file would carry its path: no match is
		// confirmed absence.
		return Identity{}
	}
}

// sortedKeys returns the map's keys in a stable order, so an ambiguous
// identity always reads the same way twice.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// identifiable reports whether the container carries the labels identity is
// established from. A relative working directory is unusable, since it is
// resolved against whatever the daemon's working directory happened to be.
func identifiable(c Container) bool {
	return len(configFiles(c)) > 0 || absolute(c.ProjectWorkingDir)
}

// servesPath reports whether the container was started from this stack.
func (c Container) servesPath(dir, file string) bool {
	for _, f := range configFiles(c) {
		if file != "" && absolute(f) && filepath.Clean(f) == file {
			return true
		}
	}
	return dir != "" && absolute(c.ProjectWorkingDir) && filepath.Clean(c.ProjectWorkingDir) == dir
}

// configFiles splits Compose's comma-separated config-file label into paths.
func configFiles(c Container) []string {
	fields := strings.Split(c.ProjectConfigFiles, ",")
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func absolute(p string) bool { return p != "" && filepath.IsAbs(p) }
