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
	// unversioned holds matching tags for which Version finds no numeric
	// run at all -- rare for a config glob, since the loose numeric-run
	// search matches almost anything with a digit, but a real construct
	// nonetheless. The spec's stated fallback orders these
	// lexicographically rather than discarding them.
	var unversioned []string
	for _, tag := range tags {
		if !c.Matches(tag) {
			continue
		}
		v, ok := c.Version(tag)
		if !ok {
			unversioned = append(unversioned, tag)
			continue
		}
		matches = append(matches, candidate{tag: tag, ver: v})
	}

	sort.Slice(matches, func(i, j int) bool {
		if cmp := matches[i].ver.Compare(matches[j].ver); cmp != 0 {
			return cmp > 0 // newest first
		}
		// Equal semver cores tie when Version truncates precision (a
		// linuxserver-style four-component tag loses its build number to
		// a three-component core): the lexicographically greater tag is
		// the newer build, so break ties descending, not ascending.
		return matches[i].tag > matches[j].tag
	})
	sort.Sort(sort.Reverse(sort.StringSlice(unversioned)))

	res := Result{Ordered: make([]string, 0, len(matches)+len(unversioned))}
	for _, m := range matches {
		res.Ordered = append(res.Ordered, m.tag)
	}
	res.Ordered = append(res.Ordered, unversioned...)

	currentVer, ok := c.Version(current)
	if !ok || len(matches) == 0 {
		return res
	}

	newest := matches[0]
	// matches[0] is already the newest by this same tie-break (see the
	// sort above): when Version's precision loss makes newest and current
	// compare as an exact semver tie, the lexicographically greater full
	// tag is the newer build and must still be offered, not silently
	// treated as "no update".
	switch cmp := newest.ver.Compare(currentVer); {
	case cmp < 0:
		return res
	case cmp == 0 && newest.tag <= current:
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
