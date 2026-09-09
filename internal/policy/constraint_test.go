package policy

import "testing"

func TestInferTrackableTags(t *testing.T) {
	tests := []struct {
		tag           string
		wantTrackable bool
		wantVariant   string
		wantPrefix    string
	}{
		{tag: "v0.107.79", wantTrackable: true, wantPrefix: "v"},
		{tag: "2026.8.3", wantTrackable: true},
		{tag: "0.9.1", wantTrackable: true},
		{tag: "8.2", wantTrackable: true},
		{tag: "18-alpine", wantTrackable: true, wantVariant: "alpine"},
		{tag: "1.27-alpine", wantTrackable: true, wantVariant: "alpine"},
		{tag: "v1.41.0", wantTrackable: true, wantPrefix: "v"},
	}
	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			got := Infer(tt.tag)
			if got.Trackable != tt.wantTrackable {
				t.Fatalf("Trackable = %v, want %v", got.Trackable, tt.wantTrackable)
			}
			if got.Variant != tt.wantVariant {
				t.Errorf("Variant = %q, want %q", got.Variant, tt.wantVariant)
			}
			if got.Prefix != tt.wantPrefix {
				t.Errorf("Prefix = %q, want %q", got.Prefix, tt.wantPrefix)
			}
		})
	}
}

// These are the tags stackmon must refuse to reason about. Suggesting
// pg15 -> pg18 would be advising a major Postgres migration in a table cell.
func TestInferRefusesOpaqueTags(t *testing.T) {
	for _, tag := range []string{"latest", "release", "pg15", "pg18", "stable", "edge", ""} {
		t.Run(tag, func(t *testing.T) {
			if got := Infer(tag); got.Trackable {
				t.Errorf("Infer(%q).Trackable = true, want false", tag)
			}
		})
	}
}

func TestConstraintMatchesOnlySameVariant(t *testing.T) {
	c := Infer("18-alpine")
	if !c.Matches("19-alpine") {
		t.Error("18-alpine constraint should match 19-alpine")
	}
	if !c.Matches("18.1-alpine") {
		t.Error("18-alpine constraint should match 18.1-alpine")
	}
	if c.Matches("18") {
		t.Error("18-alpine constraint must not match bare 18: different variant")
	}
	if c.Matches("18-bookworm") {
		t.Error("18-alpine constraint must not match 18-bookworm")
	}
}

func TestConstraintRequiresSamePrefix(t *testing.T) {
	c := Infer("v1.41.0")
	if !c.Matches("v1.42.0") {
		t.Error("v-prefixed constraint should match v1.42.0")
	}
	if c.Matches("1.42.0") {
		t.Error("v-prefixed constraint must not match an unprefixed tag")
	}
}

func TestConstraintExcludesPrereleases(t *testing.T) {
	c := Infer("1.2.3")
	for _, tag := range []string{"1.3.0-rc1", "2.0.0-beta.1", "1.4.0-alpha"} {
		if c.Matches(tag) {
			t.Errorf("constraint must exclude prerelease %q", tag)
		}
	}
}

func TestParseGlobConstraintIsTrackable(t *testing.T) {
	c := Parse("pg18*")
	if !c.Trackable {
		t.Fatal("an explicit glob constraint must be trackable")
	}
	if !c.Matches("pg18") || !c.Matches("pg18.1") {
		t.Error("pg18* should match pg18 and pg18.1")
	}
	if c.Matches("pg15") {
		t.Error("pg18* must not match pg15")
	}
}

func TestParseEmptyGlobIsNotTrackable(t *testing.T) {
	if Parse("").Trackable {
		t.Error("an empty glob must not be trackable")
	}
}
