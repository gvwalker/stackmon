package policy

import "testing"

func TestEvaluatePicksNewestMatchingTag(t *testing.T) {
	tags := []string{"v0.107.77", "v0.107.79", "v0.107.80", "latest", "v0.108.0-rc1"}
	got := Evaluate("v0.107.79", tags, Infer("v0.107.79"))
	if got.Candidate != "v0.107.80" {
		t.Errorf("Candidate = %q, want v0.107.80", got.Candidate)
	}
	if got.Kind != KindPatch {
		t.Errorf("Kind = %v, want patch", got.Kind)
	}
}

func TestEvaluateClassifiesJumpSize(t *testing.T) {
	tests := []struct {
		name     string
		current  string
		tags     []string
		wantTag  string
		wantKind Kind
	}{
		{"patch", "1.2.3", []string{"1.2.3", "1.2.4"}, "1.2.4", KindPatch},
		{"minor", "1.2.3", []string{"1.2.3", "1.3.0"}, "1.3.0", KindMinor},
		{"major", "1.2.3", []string{"1.2.3", "2.0.0"}, "2.0.0", KindMajor},
		{"major wins over minor", "1.2.3", []string{"1.3.0", "2.0.0"}, "2.0.0", KindMajor},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Evaluate(tt.current, tt.tags, Infer(tt.current))
			if got.Candidate != tt.wantTag {
				t.Errorf("Candidate = %q, want %q", got.Candidate, tt.wantTag)
			}
			if got.Kind != tt.wantKind {
				t.Errorf("Kind = %v, want %v", got.Kind, tt.wantKind)
			}
		})
	}
}

func TestEvaluateReportsNothingWhenCurrentIsNewest(t *testing.T) {
	got := Evaluate("1.2.3", []string{"1.0.0", "1.2.3"}, Infer("1.2.3"))
	if got.Candidate != "" {
		t.Errorf("Candidate = %q, want empty", got.Candidate)
	}
	if got.Kind != KindNone {
		t.Errorf("Kind = %v, want none", got.Kind)
	}
}

func TestEvaluateOnUntrackableTagReportsNothing(t *testing.T) {
	got := Evaluate("latest", []string{"latest", "1.0.0", "2.0.0"}, Infer("latest"))
	if got.Candidate != "" {
		t.Errorf("Candidate = %q, want empty for an untrackable tag", got.Candidate)
	}
}

// The user's real case: pg15 must never be told that pg18 is available
// unless a constraint explicitly opts in.
func TestEvaluateRefusesPostgresVariantJump(t *testing.T) {
	got := Evaluate("pg15", []string{"pg15", "pg16", "pg17", "pg18"}, Infer("pg15"))
	if got.Candidate != "" {
		t.Fatalf("Candidate = %q, want empty: pg15->pg18 is a migration, not an update", got.Candidate)
	}
}

func TestEvaluateWithExplicitGlobOptsIn(t *testing.T) {
	got := Evaluate("pg15", []string{"pg15", "pg18", "pg18.1"}, Parse("pg18*"))
	if got.Candidate != "pg18.1" {
		t.Errorf("Candidate = %q, want pg18.1", got.Candidate)
	}
}

func TestEvaluateOrdersCandidatesNewestFirst(t *testing.T) {
	got := Evaluate("1.0.0", []string{"1.0.0", "1.2.0", "1.1.0"}, Infer("1.0.0"))
	want := []string{"1.2.0", "1.1.0", "1.0.0"}
	if len(got.Ordered) != len(want) {
		t.Fatalf("Ordered = %v, want %v", got.Ordered, want)
	}
	for i := range want {
		if got.Ordered[i] != want[i] {
			t.Fatalf("Ordered = %v, want %v", got.Ordered, want)
		}
	}
}

func TestEvaluateVariantTagsCompareOnCore(t *testing.T) {
	got := Evaluate("18-alpine", []string{"18-alpine", "18.1-alpine", "19-alpine", "18-bookworm"}, Infer("18-alpine"))
	if got.Candidate != "19-alpine" {
		t.Errorf("Candidate = %q, want 19-alpine", got.Candidate)
	}
	if got.Kind != KindMajor {
		t.Errorf("Kind = %v, want major", got.Kind)
	}
}

// REPRODUCED (Important finding): a config glob over linuxserver-style tags
// that share a three-component semver core once Version truncates the
// fourth component must rank the newest build first in Ordered, not the
// oldest -- Ordered is documented newest-first and shown to the user by
// `show`. See TestEvaluateOffersNewerBuildWhenVersionCoresTie for the
// companion fix to Candidate on the same kind of tie.
func TestEvaluateOrdersFourComponentTagsNewestFirstOnTie(t *testing.T) {
	tags := []string{"4.0.19.2979-ls323", "4.0.19.2980-ls324"}
	got := Evaluate("4.0.19.2979-ls323", tags, Parse("4.0.*"))

	want := []string{"4.0.19.2980-ls324", "4.0.19.2979-ls323"}
	if len(got.Ordered) != len(want) || got.Ordered[0] != want[0] || got.Ordered[1] != want[1] {
		t.Fatalf("Ordered = %v, want %v (newest build first)", got.Ordered, want)
	}
}

// A glob matching a tag with no numeric run at all must still appear in
// Ordered, ranked lexicographically, rather than being silently discarded.
func TestEvaluateKeepsVersionlessGlobMatchesOrderedLexicographically(t *testing.T) {
	got := Evaluate("1.0.0", []string{"1.0.0", "1.1.0", "edge", "canary"}, Parse("*"))

	want := []string{"1.1.0", "1.0.0", "edge", "canary"}
	if len(got.Ordered) != len(want) {
		t.Fatalf("Ordered = %v, want %v", got.Ordered, want)
	}
	for i := range want {
		if got.Ordered[i] != want[i] {
			t.Fatalf("Ordered = %v, want %v", got.Ordered, want)
		}
	}
}

// LIVE SUBSTANCE: Constraint.Version truncates to at most three numeric
// components, so two linuxserver-style four-component tags whose first
// three components agree -- "4.0.19.2979-ls323" and "4.0.19.2980-ls324" --
// compare as an exact semver tie. Evaluate must not stop there: it already
// breaks the same tie on the full tag string, descending, to rank Ordered
// correctly (TestEvaluateOrdersFourComponentTagsNewestFirstOnTie); Candidate
// must use the same tie-break, or the genuinely newer build is never
// offered even though it sorts first.
func TestEvaluateOffersNewerBuildWhenVersionCoresTie(t *testing.T) {
	const current = "4.0.19.2979-ls323"
	tags := []string{current, "4.0.19.2980-ls324"}
	got := Evaluate(current, tags, Parse("4.0.19.*"))

	if got.Candidate != "4.0.19.2980-ls324" {
		t.Fatalf("Candidate = %q, want 4.0.19.2980-ls324 (newer build, tied truncated version)", got.Candidate)
	}
}

// The tie-break must not offer the current tag back to itself, and must
// not offer an older tag whose truncated version happens to tie with a
// newer one still in the running.
func TestEvaluateTieBreakNeverOffersCurrentOrOlderTag(t *testing.T) {
	const current = "4.0.19.2980-ls324"
	tags := []string{"4.0.19.2979-ls323", current}
	got := Evaluate(current, tags, Parse("4.0.19.*"))

	if got.Candidate != "" {
		t.Fatalf("Candidate = %q, want none: current tag is already the newest build", got.Candidate)
	}
}
