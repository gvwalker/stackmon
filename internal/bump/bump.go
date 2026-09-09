// Package bump rewrites image references in compose files. It never
// reserialises YAML: doing so would reformat the file and lose comments.
// Instead it replaces the exact byte range recorded during parsing.
package bump

import (
	"fmt"
	"os"
	"path/filepath"
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

// Plan computes the rewrite for one service, refusing references that must
// not be touched.
func Plan(st compose.Stack, svc compose.Service, img report.Image) (Change, error) {
	ref := svc.Ref

	if ref.Interpolated {
		return Change{}, fmt.Errorf(
			"bump: %s/%s is set from ${%s}; edit the .env file in %s rather than the compose file",
			st.Name, svc.Name, strings.Join(ref.Vars, ", "), st.Dir)
	}

	// A digest-only pin's registry digest describes only the currently
	// declared reference, not any candidate version: the registry serves
	// exactly what was asked for. There is no way to know the candidate's
	// digest, so the pin must be moved to a tag before it can advance.
	if ref.Shape == imageref.ShapeDigestOnly && img.Candidate != "" {
		return Change{}, fmt.Errorf(
			"bump: %s/%s is pinned by digest at version %s; the registry digest for candidate %s is not known (a digest-only pin's registry digest reflects only the currently declared reference), so move the pin to a tag reference before advancing from %s to %s",
			st.Name, svc.Name, img.Version, img.Candidate, img.Version, img.Candidate)
	}

	tag, digest := ref.Tag, ref.Digest

	if img.Candidate != "" {
		tag = img.Candidate
	}
	if digest != "" && img.RegistryDigest != "" {
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

// Apply writes the change, aborting if the file no longer matches what was
// observed, so a concurrent edit cannot be clobbered.
func Apply(c Change) error {
	data, err := os.ReadFile(c.Path)
	if err != nil {
		return fmt.Errorf("bump: reading %s: %w", c.Path, err)
	}

	if c.Offset < 0 || c.Offset+c.Length > len(data) {
		return fmt.Errorf("bump: %s changed since it was checked; re-run check", c.Path)
	}
	if got := string(data[c.Offset : c.Offset+c.Length]); got != c.Old {
		return fmt.Errorf("bump: %s changed since it was checked (found %q where %q was expected); re-run check", c.Path, got, c.Old)
	}

	info, err := os.Stat(c.Path)
	if err != nil {
		return fmt.Errorf("bump: stat %s: %w", c.Path, err)
	}

	var out []byte
	out = append(out, data[:c.Offset]...)
	out = append(out, c.New...)
	out = append(out, data[c.Offset+c.Length:]...)

	dir := filepath.Dir(c.Path)
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
	if err := os.Rename(tmpName, c.Path); err != nil {
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
	if c.Offset+c.Length > len(data) {
		return "", fmt.Errorf("bump: %s changed since it was checked; re-run check", c.Path)
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
