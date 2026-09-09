// Package policy decides what counts as an available update. It performs no
// I/O, so every rule here is exhaustively unit-tested.
package policy

import (
	"path"
	"regexp"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// tagPattern splits a tag into an optional "v" prefix, a numeric semver
// core, and an optional variant suffix: "v1.2.3", "18-alpine", "1.27-alpine".
// The prefix group is deliberately narrow ("v?", not any letter run): a
// wider prefix would let an opaque tag like "pg15" parse as prefix "pg" +
// core "15", which is exactly the guess stackmon must refuse to make.
var tagPattern = regexp.MustCompile(`^(v?)(\d+(?:\.\d+){0,2})(?:-([A-Za-z][A-Za-z0-9.]*))?$`)

// versionPattern extracts the first numeric run from a tag regardless of
// what precedes it, so an explicit config glob such as "pg18*" can still be
// ordered by version even though its tags don't match tagPattern's narrow
// prefix.
var versionPattern = regexp.MustCompile(`(\d+(?:\.\d+){0,2})`)

// Constraint decides which tags compete with the current one.
type Constraint struct {
	// Prefix is a leading letter run such as "v".
	Prefix string
	// Variant is a trailing suffix such as "alpine".
	Variant string
	// Glob, when set, replaces inference entirely and comes from config.
	Glob string
	// Trackable is false when stackmon declines to reason about the tag.
	Trackable bool
}

// Infer derives a constraint from the current tag. Tags whose core is not
// semver — "latest", "release", "pg15" — are not trackable: guessing that
// pg15 supersedes into pg18 would be advising a major database migration.
func Infer(tag string) Constraint {
	m := tagPattern.FindStringSubmatch(tag)
	if m == nil {
		return Constraint{}
	}
	return Constraint{
		Prefix:    m[1],
		Variant:   m[3],
		Trackable: true,
	}
}

// Parse builds a constraint from an explicit config glob.
func Parse(glob string) Constraint {
	if strings.TrimSpace(glob) == "" {
		return Constraint{}
	}
	return Constraint{Glob: glob, Trackable: true}
}

// Matches reports whether a candidate tag competes with the current one.
func (c Constraint) Matches(tag string) bool {
	if !c.Trackable {
		return false
	}
	if c.Glob != "" {
		ok, err := path.Match(c.Glob, tag)
		return err == nil && ok
	}

	m := tagPattern.FindStringSubmatch(tag)
	if m == nil {
		return false
	}
	if m[1] != c.Prefix || m[3] != c.Variant {
		return false
	}
	// A prerelease core cannot reach here: tagPattern's variant group would
	// have captured "rc1" as the variant, which differs from c.Variant unless
	// the current tag is itself a prerelease of the same channel.
	return true
}

// Version extracts a comparable version from a tag, if one is present. It
// searches for the first numeric run rather than anchoring on tagPattern,
// so glob-matched tags like "pg18.1" — which never match the "v?"-anchored
// tagPattern — still yield a version for ordering.
func (c Constraint) Version(tag string) (*semver.Version, bool) {
	m := versionPattern.FindStringSubmatch(tag)
	if m == nil {
		return nil, false
	}
	v, err := semver.NewVersion(m[1])
	if err != nil {
		return nil, false
	}
	return v, true
}
