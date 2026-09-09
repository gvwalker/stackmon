package imageref

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		name                                        string
		raw, resolved                               string
		wantRegistry, wantRepo, wantTag, wantDigest string
		wantShape                                   Shape
		wantInterp                                  bool
	}{
		{
			name: "docker hub tag only",
			raw:  "redis:8.2", resolved: "redis:8.2",
			wantRegistry: "index.docker.io", wantRepo: "library/redis",
			wantTag: "8.2", wantShape: ShapeTagOnly,
		},
		{
			name: "bare name defaults to latest",
			raw:  "nginx", resolved: "nginx",
			wantRegistry: "index.docker.io", wantRepo: "library/nginx",
			wantTag: "latest", wantShape: ShapeTagOnly,
		},
		{
			name: "lscr floating tag",
			raw:  "lscr.io/linuxserver/sonarr:latest", resolved: "lscr.io/linuxserver/sonarr:latest",
			wantRegistry: "lscr.io", wantRepo: "linuxserver/sonarr",
			wantTag: "latest", wantShape: ShapeTagOnly,
		},
		{
			name:         "tag and digest",
			raw:          "adguard/adguardhome:v0.107.79@sha256:aba9e3bf0613be3ba3755e1fc311b126e2c24bec25e18b6483894a88283074f0",
			resolved:     "adguard/adguardhome:v0.107.79@sha256:aba9e3bf0613be3ba3755e1fc311b126e2c24bec25e18b6483894a88283074f0",
			wantRegistry: "index.docker.io", wantRepo: "adguard/adguardhome",
			wantTag:    "v0.107.79",
			wantDigest: "sha256:aba9e3bf0613be3ba3755e1fc311b126e2c24bec25e18b6483894a88283074f0",
			wantShape:  ShapeTagDigest,
		},
		{
			name:         "digest only",
			raw:          "traefik@sha256:9c3b91d5fb7770853ca5c1124a23c34bf2d9b47ffaebeab2614cbaf410dcb2ac",
			resolved:     "traefik@sha256:9c3b91d5fb7770853ca5c1124a23c34bf2d9b47ffaebeab2614cbaf410dcb2ac",
			wantRegistry: "index.docker.io", wantRepo: "library/traefik",
			wantDigest: "sha256:9c3b91d5fb7770853ca5c1124a23c34bf2d9b47ffaebeab2614cbaf410dcb2ac",
			wantShape:  ShapeDigestOnly,
		},
		{
			name: "ghcr with variant suffix tag",
			raw:  "ghcr.io/vectorize-io/hindsight:0.9.1", resolved: "ghcr.io/vectorize-io/hindsight:0.9.1",
			wantRegistry: "ghcr.io", wantRepo: "vectorize-io/hindsight",
			wantTag: "0.9.1", wantShape: ShapeTagOnly,
		},
		{
			name: "registry port without tag",
			raw:  "localhost:5000/app", resolved: "localhost:5000/app",
			wantRegistry: "localhost:5000", wantRepo: "app",
			wantTag: "latest", wantShape: ShapeTagOnly,
		},
		{
			name: "registry port with tag",
			raw:  "localhost:5000/app:1.2", resolved: "localhost:5000/app:1.2",
			wantRegistry: "localhost:5000", wantRepo: "app",
			wantTag: "1.2", wantShape: ShapeTagOnly,
		},
		{
			name:         "interpolated with default",
			raw:          "ghcr.io/karakeep-app/karakeep:${KARAKEEP_VERSION:-release}",
			resolved:     "ghcr.io/karakeep-app/karakeep:release",
			wantRegistry: "ghcr.io", wantRepo: "karakeep-app/karakeep",
			wantTag: "release", wantShape: ShapeTagOnly, wantInterp: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.raw, tt.resolved)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tt.resolved, err)
			}
			if got.Registry != tt.wantRegistry {
				t.Errorf("Registry = %q, want %q", got.Registry, tt.wantRegistry)
			}
			if got.Repository != tt.wantRepo {
				t.Errorf("Repository = %q, want %q", got.Repository, tt.wantRepo)
			}
			if got.Tag != tt.wantTag {
				t.Errorf("Tag = %q, want %q", got.Tag, tt.wantTag)
			}
			if got.Digest != tt.wantDigest {
				t.Errorf("Digest = %q, want %q", got.Digest, tt.wantDigest)
			}
			if got.Shape != tt.wantShape {
				t.Errorf("Shape = %v, want %v", got.Shape, tt.wantShape)
			}
			if got.Interpolated != tt.wantInterp {
				t.Errorf("Interpolated = %v, want %v", got.Interpolated, tt.wantInterp)
			}
			if !tt.wantInterp && len(got.Vars) != 0 {
				t.Errorf("Vars = %v, want none for an uninterpolated ref", got.Vars)
			}
		})
	}
}

func TestParseInterpolatedRecordsVars(t *testing.T) {
	got, err := Parse("ghcr.io/x/y:${A_VERSION:-release}", "ghcr.io/x/y:release")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	want := []string{"A_VERSION"}
	if len(got.Vars) != 1 || got.Vars[0] != want[0] {
		t.Errorf("Vars = %v, want %v", got.Vars, want)
	}
}

func TestParseVarsIgnoresEscapedDollar(t *testing.T) {
	// Compose writes $$ for a literal dollar; it names no variable, so a ref
	// carrying one must not report it as a dependency.
	got, err := Parse("ghcr.io/x/y:$${NOT_A_VAR}", "ghcr.io/x/y:release")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(got.Vars) != 0 {
		t.Errorf("Vars = %v, want none", got.Vars)
	}
}

func TestParseRejectsEmpty(t *testing.T) {
	if _, err := Parse("", ""); err == nil {
		t.Fatal("Parse(\"\") = nil error, want error")
	}
}
