// Package report assembles the comparison of declared, running, and registry
// state into a serialisable result. It is the boundary between the engine and
// any presentation layer.
package report

import (
	"time"

	"github.com/gvwalker/stackmon/internal/imageref"
	"github.com/gvwalker/stackmon/internal/policy"
)

// Status is the single headline verdict for an image.
type Status string

// Statuses, in precedence order: the first that applies wins.
const (
	StatusUnknown         Status = "unknown"
	StatusUpdateAvailable Status = "update-available"
	StatusDigestDrift     Status = "digest-drift"
	StatusNotDeployed     Status = "not-deployed"
	StatusStaleDeployment Status = "stale-deployment"
	StatusNotRunning      Status = "not-running"
	StatusCurrent         Status = "current"
)

// Replica is one running container of a Service. Each Replica keeps its own
// digest set: agreement by one Replica is not agreement by the Service, so
// their digests are never unioned.
type Replica struct {
	// Image is the reference the container was started from.
	Image string `json:"image,omitempty"`
	// Digests is every manifest digest Docker recorded for this container's
	// image. Empty when the image has none (built locally, or the lookup
	// failed), which leaves the Replica unjudgeable rather than wrong.
	Digests []string `json:"digests,omitempty"`
}

// ReplicaComparison summarises how a Service's Replicas compared against the
// digest they were judged by.
type ReplicaComparison struct {
	// Compared is that digest: the declared pin when the reference has one,
	// otherwise whatever the registry currently serves.
	Compared string `json:"compared,omitempty"`
	// Matching, Mismatching and Unknown count the Replicas. Unknown counts
	// Replicas with no recorded digest to compare, so they are neither
	// agreement nor disagreement.
	Matching    int `json:"matching"`
	Mismatching int `json:"mismatching"`
	Unknown     int `json:"unknown"`
	// Incomplete is true when at least one Replica could not be judged, so
	// the comparison says less than the Service's full state.
	Incomplete bool `json:"incomplete"`
}

// Image is one service's image and everything learned about it.
type Image struct {
	Stack   string       `json:"stack"`
	Service string       `json:"service"`
	Ref     imageref.Ref `json:"ref"`

	// DeclaredDigest is the digest written in the compose file, empty for a
	// tag-only reference.
	DeclaredDigest string `json:"declared_digest,omitempty"`
	// Project is the Docker Compose project this service's running state was
	// read from, empty when none is running this stack's compose file.
	Project string `json:"project,omitempty"`
	// ProjectBound is true when Project came from an explicit binding
	// rather than from matching the stack's paths against Docker's labels.
	ProjectBound bool `json:"project_bound"`
	// IdentityNote explains a project that could not be established. It is a
	// distinct field from Err because an unresolved identity does not
	// invalidate the registry findings, which stay worth reporting.
	IdentityNote string `json:"identity_note,omitempty"`
	// Running is true when at least one Replica of the service was found.
	// A Replica with no RepoDigests entry (built locally, docker load'd, or
	// a failed lookup) is still running: only DockerChecked && !Running
	// means no container exists.
	Running bool `json:"running"`
	// Replicas are the running containers of this service, in a stable
	// order.
	Replicas []Replica `json:"replicas,omitempty"`
	// ReplicaComparison is how those Replicas compared.
	ReplicaComparison ReplicaComparison `json:"replica_comparison"`
	// RegistryDigest is what the registry serves for the declared reference.
	RegistryDigest string `json:"registry_digest,omitempty"`
	// DockerChecked records whether the daemon was reachable, so that an
	// absent running digest is not mistaken for a stopped container.
	DockerChecked bool `json:"docker_checked"`

	// Version is the current version: the tag, or for a digest-only pin the
	// org.opencontainers.image.version label.
	Version string `json:"version,omitempty"`
	// Candidate is the newest tag that supersedes Version.
	Candidate string `json:"candidate,omitempty"`
	// CandidateDigest is the registry digest for Candidate, resolved
	// separately from RegistryDigest: RegistryDigest describes only the
	// declared reference, never a different candidate version. Empty when
	// there is no candidate or its digest could not be resolved.
	CandidateDigest string      `json:"candidate_digest,omitempty"`
	Kind            policy.Kind `json:"-"`
	// KindName is Kind rendered for JSON consumers.
	KindName string   `json:"kind,omitempty"`
	Ordered  []string `json:"ordered,omitempty"`

	// Created and Revision describe the currently declared image; the
	// Registry* fields describe what the registry now serves. Comparing them
	// explains a digest change without pulling anything.
	Created          time.Time `json:"created,omitempty"`
	Revision         string    `json:"revision,omitempty"`
	RegistryCreated  time.Time `json:"registry_created,omitempty"`
	RegistryRevision string    `json:"registry_revision,omitempty"`

	// Source is the org.opencontainers.image.source URL, if any.
	Source string `json:"source,omitempty"`
	// NotesRepo is the resolved GitHub "owner/name", if any.
	NotesRepo string `json:"notes_repo,omitempty"`

	// Status is the headline verdict; Statuses lists everything that applied.
	Status   Status   `json:"status"`
	Statuses []Status `json:"statuses,omitempty"`
	// Err explains a StatusUnknown row.
	Err string `json:"error,omitempty"`
}

// Report is the whole run.
type Report struct {
	Generated time.Time `json:"generated"`
	// DockerAvailable is false when the socket was absent, in which case the
	// running-container signal is missing from every row.
	DockerAvailable bool    `json:"docker_available"`
	Images          []Image `json:"images"`
}

// HasUpdates reports whether any image has a newer version available. Drift
// alone does not count, because floating tags drift continuously by design.
func (r Report) HasUpdates() bool {
	for _, i := range r.Images {
		if i.Status == StatusUpdateAvailable {
			return true
		}
	}
	return false
}

// HasDrift reports whether any image's digest has moved.
func (r Report) HasDrift() bool {
	for _, i := range r.Images {
		if i.Status == StatusDigestDrift {
			return true
		}
	}
	return false
}

// applicable lists every status true of an image, in precedence order.
func applicable(i Image) []Status {
	var out []Status

	if i.Err != "" {
		return []Status{StatusUnknown}
	}
	if i.Candidate != "" {
		out = append(out, StatusUpdateAvailable)
	}
	// Only a reference that declares a digest can drift against the registry.
	if i.DeclaredDigest != "" && i.RegistryDigest != "" && i.DeclaredDigest != i.RegistryDigest {
		out = append(out, StatusDigestDrift)
	}
	// An ambiguous or unverifiable project identity means the running state
	// was never established, which is not the same as nothing running. The
	// registry findings above survive; "not running" would not be a fact.
	if i.DockerChecked && i.IdentityNote != "" {
		out = append(out, StatusUnknown)
	}
	if i.DockerChecked && i.Running {
		// One Replica behind is the Service behind: the deployment is
		// mixed, and the replica that agrees does not make it whole.
		if i.ReplicaComparison.Mismatching > 0 {
			if i.DeclaredDigest != "" {
				out = append(out, StatusNotDeployed)
			} else {
				out = append(out, StatusStaleDeployment)
			}
		}
		// A Replica that could not be judged leaves the comparison short of
		// the Service's full state, so it cannot be "current". A known
		// mismatch above is still the more actionable finding and stays
		// the headline.
		if i.ReplicaComparison.Incomplete {
			out = append(out, StatusUnknown)
		}
	}
	if i.DockerChecked && !i.Running && i.IdentityNote == "" {
		out = append(out, StatusNotRunning)
	}
	if len(out) == 0 {
		out = append(out, StatusCurrent)
	}
	return out
}

// containsDigest reports whether digest appears anywhere in set. Docker
// records one RepoDigests entry per manifest digest an image has ever been
// pulled under, so the running or declared side of a comparison may
// legitimately be a set rather than a single value.
func containsDigest(set []string, digest string) bool {
	for _, d := range set {
		if d == digest {
			return true
		}
	}
	return false
}

// Decide returns the headline status: the most actionable one that applies.
func Decide(i Image) Status {
	return applicable(i)[0]
}
