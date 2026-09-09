package report

import "testing"

func TestDecidePrecedence(t *testing.T) {
	tests := []struct {
		name string
		img  Image
		want Status
	}{
		{
			name: "error beats everything",
			img:  Image{Err: "boom", Candidate: "2.0.0", DeclaredDigest: "sha256:a", RegistryDigest: "sha256:b"},
			want: StatusUnknown,
		},
		{
			name: "update beats drift",
			img:  Image{Candidate: "2.0.0", DeclaredDigest: "sha256:a", RegistryDigest: "sha256:b"},
			want: StatusUpdateAvailable,
		},
		{
			name: "drift beats not-deployed",
			img:  Image{DeclaredDigest: "sha256:a", RegistryDigest: "sha256:b", RunningDigest: "sha256:c"},
			want: StatusDigestDrift,
		},
		{
			name: "not deployed when running differs from declared",
			img:  Image{DeclaredDigest: "sha256:a", RegistryDigest: "sha256:a", RunningDigest: "sha256:c"},
			want: StatusNotDeployed,
		},
		{
			name: "stale deployment for a floating tag",
			img:  Image{RegistryDigest: "sha256:b", RunningDigest: "sha256:c", DockerChecked: true},
			want: StatusStaleDeployment,
		},
		{
			name: "not running when docker was checked and found nothing",
			img:  Image{DeclaredDigest: "sha256:a", RegistryDigest: "sha256:a", DockerChecked: true},
			want: StatusNotRunning,
		},
		{
			name: "current when everything agrees",
			img:  Image{DeclaredDigest: "sha256:a", RegistryDigest: "sha256:a", RunningDigest: "sha256:a", DockerChecked: true},
			want: StatusCurrent,
		},
		{
			name: "current with no docker signal at all",
			img:  Image{DeclaredDigest: "sha256:a", RegistryDigest: "sha256:a"},
			want: StatusCurrent,
		},
		{
			name: "floating tag matching the registry is current",
			img:  Image{RegistryDigest: "sha256:b", RunningDigest: "sha256:b", DockerChecked: true},
			want: StatusCurrent,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Decide(tt.img); got != tt.want {
				t.Errorf("Decide() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHasUpdatesIgnoresDrift(t *testing.T) {
	r := Report{Images: []Image{{Status: StatusDigestDrift}}}
	if r.HasUpdates() {
		t.Error("HasUpdates() = true for drift alone; --fail-on-update must not fire on drift")
	}
	if !r.HasDrift() {
		t.Error("HasDrift() = false, want true")
	}
}

func TestHasUpdatesTrueForUpdateAvailable(t *testing.T) {
	r := Report{Images: []Image{{Status: StatusCurrent}, {Status: StatusUpdateAvailable}}}
	if !r.HasUpdates() {
		t.Error("HasUpdates() = false, want true")
	}
}
