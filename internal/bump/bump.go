// Package bump rewrites image references in compose files. It never
// reserialises YAML: doing so would reformat the file and lose comments.
// Instead it replaces the exact byte range recorded during parsing.
package bump

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gvwalker/stackmon/internal/compose"
	"github.com/gvwalker/stackmon/internal/imageref"
	"github.com/gvwalker/stackmon/internal/report"
)

// Change is a single planned rewrite.
type Change struct {
	Path   string
	Offset int
	Length int
	// Old is the exact text expected at Offset; Apply refuses if it differs.
	Old string
	New string
}

// Options controls how Plan treats otherwise floating tagged references.
type Options struct {
	// Digest adds the candidate's resolved digest to a tag-only pin. It is
	// deliberately opt-in because it changes a floating tag into a pin.
	Digest bool
}

// Plan computes the default rewrite for one service, refusing references that
// must not be touched.
func Plan(st compose.Stack, svc compose.Service, img report.Image) (Change, error) {
	return PlanWithOptions(st, svc, img, Options{})
}

// PlanWithOptions computes the rewrite for one service with optional bump
// behavior enabled.
func PlanWithOptions(st compose.Stack, svc compose.Service, img report.Image, opts Options) (Change, error) {
	ref := svc.Ref

	if ref.Interpolated {
		return Change{}, fmt.Errorf(
			"bump: %s/%s is set from ${%s}; edit the .env file in %s rather than the compose file",
			st.Name, svc.Name, strings.Join(ref.Vars, ", "), st.Dir)
	}

	tag, digest := ref.Tag, ref.Digest

	switch {
	case img.Candidate != "":
		tag = img.Candidate
		if ref.Shape != imageref.ShapeTagOnly || opts.Digest {
			// A tag+digest or digest-only pin also carries a digest, and it
			// must be the candidate's, not the declared reference's:
			// RegistryDigest is what the registry serves for the
			// reference exactly as currently pinned, never for a
			// different version. Without the candidate's own digest there
			// is no safe value to write.
			if img.CandidateDigest == "" {
				current := ref.Tag
				if ref.Shape == imageref.ShapeDigestOnly {
					current = img.Version
				}
				return Change{}, fmt.Errorf(
					"bump: %s/%s: candidate %s's digest could not be resolved (currently at %s); the pin cannot be safely advanced without it",
					st.Name, svc.Name, img.Candidate, current)
			}
			digest = img.CandidateDigest
		}
	case digest != "" && img.RegistryDigest != "":
		// No candidate: only the digest may have drifted under the same
		// declared version, so the registry's digest for that reference is
		// exactly what's needed.
		digest = img.RegistryDigest
	}

	// A tag-only reference with no candidate is floating: there is nothing to
	// advance to, and adding a digest would change the update policy.
	if ref.Shape == imageref.ShapeTagOnly && img.Candidate == "" {
		return Change{}, fmt.Errorf(
			"bump: %s/%s tracks the floating tag %q; there is no newer version to move to, and pinning it would change how it updates",
			st.Name, svc.Name, ref.Tag)
	}

	next := rebuild(ref, tag, digest)
	if next == ref.Raw {
		return Change{}, fmt.Errorf("bump: %s/%s is already at %s", st.Name, svc.Name, ref.Raw)
	}

	// This is the only code that writes to a live production compose
	// file; it must be impossible to write a reference the tool cannot
	// read back. The digest values above come straight from exported,
	// unvalidated string fields, so confirm the rebuilt reference actually
	// parses before returning it.
	if _, err := imageref.Parse(next, next); err != nil {
		return Change{}, fmt.Errorf("bump: %s/%s: refusing to write an unparseable reference %q: %w", st.Name, svc.Name, next, err)
	}

	return Change{
		Path:   filepath.Join(st.Dir, st.File),
		Offset: svc.Offset,
		Length: svc.Length,
		Old:    ref.Raw,
		New:    next,
	}, nil
}

// rebuild reassembles a reference from its parts.
func rebuild(ref imageref.Ref, tag, digest string) string {
	var b strings.Builder

	// Preserve the registry exactly as written: "redis" must not become
	// "index.docker.io/library/redis".
	name := ref.Raw
	if i := strings.Index(name, "@"); i >= 0 {
		name = name[:i]
	}
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name = name[:i]
	}
	b.WriteString(name)

	if tag != "" && ref.Shape != imageref.ShapeDigestOnly {
		b.WriteString(":")
		b.WriteString(tag)
	}
	if digest != "" {
		b.WriteString("@")
		b.WriteString(digest)
	}
	return b.String()
}

// validateRange checks that c's range is well-formed and still fits within
// data, before either Apply or Diff slices by it. Change's fields are
// exported and the type is designed to survive between a check and a later
// bump, so a hostile or stale value (a negative Offset, or a non-positive
// Length that would invert the slice bounds) must be rejected cleanly
// rather than panic.
func validateRange(c Change, dataLen int) error {
	if c.Length <= 0 {
		return fmt.Errorf("bump: %s has an invalid range (length %d); re-run check", c.Path, c.Length)
	}
	if c.Offset < 0 || c.Offset+c.Length > dataLen {
		return fmt.Errorf("bump: %s changed since it was checked; re-run check", c.Path)
	}
	return nil
}

// Apply writes the change, aborting if the file no longer matches what was
// observed, so a concurrent edit cannot be clobbered.
func Apply(c Change) error { return ApplyAll([]Change{c}) }

// ApplyAll writes changes after checking every source range against the same
// original file contents. Replacements are applied from the end of each file
// towards its beginning, so a longer earlier pin cannot invalidate a later
// service's offsets. Nothing is written for a file until all of that file's
// changes pass the stale-file checks.
func ApplyAll(changes []Change) error {
	byPath := make(map[string][]Change)
	for _, c := range changes {
		byPath[c.Path] = append(byPath[c.Path], c)
	}
	for path, group := range byPath {
		if err := applyFile(path, group); err != nil {
			return err
		}
	}
	return nil
}

func applyFile(path string, changes []Change) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("bump: reading %s: %w", path, err)
	}
	for _, c := range changes {
		if err := validateRange(c, len(data)); err != nil {
			return err
		}
		if got := string(data[c.Offset : c.Offset+c.Length]); got != c.Old {
			return fmt.Errorf("bump: %s changed since it was checked (found %q where %q was expected); re-run check", c.Path, got, c.Old)
		}
	}

	sort.Slice(changes, func(i, j int) bool { return changes[i].Offset > changes[j].Offset })
	for i := 1; i < len(changes); i++ {
		if changes[i-1].Offset < changes[i].Offset+changes[i].Length {
			return fmt.Errorf("bump: %s has overlapping planned changes; re-run check", path)
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("bump: stat %s: %w", path, err)
	}

	out := append([]byte(nil), data...)
	for _, c := range changes {
		out = append(out[:c.Offset], append([]byte(c.New), out[c.Offset+c.Length:]...)...)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".stackmon-*.yml")
	if err != nil {
		return fmt.Errorf("bump: creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("bump: writing temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("bump: syncing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("bump: closing temp file: %w", err)
	}
	if err := os.Chmod(tmpName, info.Mode().Perm()); err != nil {
		return fmt.Errorf("bump: preserving mode: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("bump: renaming into place: %w", err)
	}
	return nil
}

// Diff renders the change as a minimal unified diff of the affected line.
func Diff(c Change) (string, error) {
	data, err := os.ReadFile(c.Path)
	if err != nil {
		return "", fmt.Errorf("bump: reading %s: %w", c.Path, err)
	}
	if err := validateRange(c, len(data)); err != nil {
		return "", err
	}
	// A dry-run that lies about the pending change is worse than no
	// dry-run. Apply the same staleness check Apply uses before rendering
	// a preview, so a concurrent edit is refused rather than shown as a
	// no-op or a diff of the wrong text.
	if got := string(data[c.Offset : c.Offset+c.Length]); got != c.Old {
		return "", fmt.Errorf("bump: %s changed since it was checked (found %q where %q was expected); re-run check", c.Path, got, c.Old)
	}

	start := strings.LastIndex(string(data[:c.Offset]), "\n") + 1
	end := c.Offset + c.Length
	if i := strings.Index(string(data[end:]), "\n"); i >= 0 {
		end += i
	}

	oldLine := string(data[start:end])
	newLine := strings.Replace(oldLine, c.Old, c.New, 1)

	return fmt.Sprintf("--- %s\n+++ %s\n-%s\n+%s\n", c.Path, c.Path, oldLine, newLine), nil
}
