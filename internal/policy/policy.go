package policy

import (
	"sort"

	"github.com/Masterminds/semver/v3"
)

// Kind is how large an update is.
type Kind int

const (
	// KindNone means no update is available.
	KindNone Kind = iota
	KindPatch
	KindMinor
	KindMajor
)

func (k Kind) String() string {
	switch k {
	case KindPatch:
		return "patch"
	case KindMinor:
		return "minor"
	case KindMajor:
		return "major"
	}
	return "none"
}

// Result is the verdict for one image.
type Result struct {
	// Candidate is the newest matching tag, or "" when none supersedes the
	// current one.
	Candidate string
	Kind      Kind
	// Ordered is every matching tag, newest first. It is what `show`
	// displays to explain the choice.
	Ordered []string
}

// Evaluate ranks the tags matching c and reports the newest that supersedes
// current. An untrackable constraint always yields an empty Result.
func Evaluate(current string, tags []string, c Constraint) Result {
	if !c.Trackable {
		return Result{}
	}

	type candidate struct {
		tag string
		ver *semver.Version
	}

	var matches []candidate
	for _, tag := range tags {
		if !c.Matches(tag) {
			continue
		}
		v, ok := c.Version(tag)
		if !ok {
			continue
		}
		matches = append(matches, candidate{tag: tag, ver: v})
	}

	sort.Slice(matches, func(i, j int) bool {
		if cmp := matches[i].ver.Compare(matches[j].ver); cmp != 0 {
			return cmp > 0 // newest first
		}
		return matches[i].tag < matches[j].tag
	})

	res := Result{Ordered: make([]string, 0, len(matches))}
	for _, m := range matches {
		res.Ordered = append(res.Ordered, m.tag)
	}

	currentVer, ok := c.Version(current)
	if !ok || len(matches) == 0 {
		return res
	}

	newest := matches[0]
	if newest.ver.Compare(currentVer) <= 0 {
		return res
	}

	res.Candidate = newest.tag
	res.Kind = classify(currentVer, newest.ver)
	return res
}

// classify reports how large the jump between two versions is.
func classify(from, to *semver.Version) Kind {
	switch {
	case to.Major() != from.Major():
		return KindMajor
	case to.Minor() != from.Minor():
		return KindMinor
	default:
		return KindPatch
	}
}
