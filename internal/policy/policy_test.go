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
