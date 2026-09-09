// Package imageref parses container image references into their parts and
// classifies their syntactic shape. It contains no policy: whether a tag is
// worth tracking is decided by package policy.
package imageref

import (
	"errors"
	"regexp"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
)

// Shape is the syntactic form of a reference.
type Shape int

const (
	// ShapeTagOnly is "repo:tag" with no digest.
	ShapeTagOnly Shape = iota
	// ShapeTagDigest is "repo:tag@sha256:...".
	ShapeTagDigest
	// ShapeDigestOnly is "repo@sha256:..." with no tag.
	ShapeDigestOnly
)

func (s Shape) String() string {
	switch s {
	case ShapeTagOnly:
		return "tag"
	case ShapeTagDigest:
		return "tag+digest"
	case ShapeDigestOnly:
		return "digest"
	}
	return "unknown"
}

// Ref is a parsed image reference.
type Ref struct {
	// Raw is the reference exactly as written in the compose file and may
	// contain unexpanded ${VAR} expressions.
	Raw string
	// Resolved is Raw after compose interpolation. All fields below derive
	// from it.
	Resolved     string
	Registry     string
	Repository   string
	Tag          string
	Digest       string
	Shape        Shape
	Interpolated bool
	// Vars names the environment variables referenced by Raw, if any.
	Vars []string
}

func (r Ref) String() string { return r.Resolved }

var varPattern = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)`)

// Parse splits a reference into its parts. raw is the compose-file text and
// resolved is that text after interpolation; pass the same string for both
// when no interpolation applies.
func Parse(raw, resolved string) (Ref, error) {
	if strings.TrimSpace(resolved) == "" {
		return Ref{}, errors.New("imageref: empty reference")
	}

	parsed, err := name.ParseReference(resolved)
	if err != nil {
		return Ref{}, err
	}

	r := Ref{
		Raw:          raw,
		Resolved:     resolved,
		Registry:     parsed.Context().RegistryStr(),
		Repository:   parsed.Context().RepositoryStr(),
		Interpolated: raw != resolved,
	}

	if r.Interpolated {
		for _, m := range varPattern.FindAllStringSubmatch(raw, -1) {
			r.Vars = append(r.Vars, m[1])
		}
	}

	// Split on the digest first, because a ref may carry both.
	rest := resolved
	if i := strings.Index(rest, "@"); i >= 0 {
		r.Digest = rest[i+1:]
		rest = rest[:i]
	}
	// A colon after the last slash is a tag; a colon before it is a port.
	if i := strings.LastIndex(rest, ":"); i > strings.LastIndex(rest, "/") {
		r.Tag = rest[i+1:]
	}

	switch {
	case r.Digest != "" && r.Tag != "":
		r.Shape = ShapeTagDigest
	case r.Digest != "":
		r.Shape = ShapeDigestOnly
	default:
		r.Shape = ShapeTagOnly
		if r.Tag == "" {
			// A bare "repo" means "repo:latest".
			r.Tag = "latest"
		}
	}

	return r, nil
}
