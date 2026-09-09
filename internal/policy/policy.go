package policy

import (
	"math/big"
	"regexp"
	"sort"
	"strings"

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

// tagRunPattern finds each maximal dot-joined numeric run in a tag, in
// order: "4.0.19.999-ls400" yields ["4.0.19.999", "400"], one run for the
// dotted core and a second for the build-number suffix.
var tagRunPattern = regexp.MustCompile(`\d+(?:\.\d+)*`)

// compareTags orders two tags by their numeric runs rather than by raw
// string value, so a digit-width change ("999" vs "1000") or an
// implicit-zero alias ("18" vs "18.0") compares the way a human reading
// build numbers would. It is the single source of truth for "which tag is
// newer": both the matches sort below and Evaluate's candidate tie-break
// call it, so they can never disagree about ordering.
//
// Runs are compared in order; within a run, dot-separated components
// compare numerically with the shorter run zero-padded, so "18" == "18.0"
// (the trailing zero adds no information) while "19" < "19.1". A run
// present in one tag with no counterpart in the other only decides the
// comparison if every shared run tied exactly, in which case the tag with
// the extra run is newer (it carries strictly more precision). Tags with
// no numeric run at all fall back to a plain string compare, so ordering
// stays defined for genuinely opaque tags reachable through a glob
// constraint.
func compareTags(a, b string) int {
	runsA := tagRunPattern.FindAllString(a, -1)
	runsB := tagRunPattern.FindAllString(b, -1)
	if len(runsA) == 0 && len(runsB) == 0 {
		return strings.Compare(a, b)
	}

	for i := range min(len(runsA), len(runsB)) {
		if cmp := compareNumericRun(runsA[i], runsB[i]); cmp != 0 {
			return cmp
		}
	}
	return len(runsA) - len(runsB)
}

// compareNumericRun compares two dot-joined numeric runs component-wise,
// zero-padding the shorter to the longer's length.
func compareNumericRun(a, b string) int {
	partsA := strings.Split(a, ".")
	partsB := strings.Split(b, ".")
	n := max(len(partsA), len(partsB))
	for i := range n {
		cmp := runComponent(partsA, i).Cmp(runComponent(partsB, i))
		if cmp != 0 {
			return cmp
		}
	}
	return 0
}

// runComponent returns the i'th dot-separated component of parts as a
// big.Int, or zero when parts is too short -- an absent trailing component
// is an implicit zero, not a smaller-but-present digit.
func runComponent(parts []string, i int) *big.Int {
	if i >= len(parts) {
		return new(big.Int)
	}
	n, ok := new(big.Int).SetString(parts[i], 10)
	if !ok {
		return new(big.Int) // unreachable: tagRunPattern guarantees digits only
	}
	return n
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
		// a three-component core): break the tie with the numeric-aware
		// tag comparator, descending, so a digit-width change ("999" vs
		// "1000") still ranks the newer build first.
		return compareTags(matches[i].tag, matches[j].tag) > 0
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
	// compare as an exact semver tie, the same numeric-aware comparator
	// used to sort decides whether the full tag is a genuinely newer
	// build (offer it) or ties exactly -- an implicit-zero alias like
	// "18.0" for "18" -- in which case there is nothing to offer.
	switch cmp := newest.ver.Compare(currentVer); {
	case cmp < 0:
		return res
	case cmp == 0 && compareTags(newest.tag, current) <= 0:
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
