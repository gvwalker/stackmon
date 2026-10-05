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
	// failed); DigestError says which of those it was.
	Digests []string `json:"digests,omitempty"`
	// DigestError is the lookup failure that left Digests empty, when there
	// was one. Empty means the lookup either succeeded or was never needed,
	// so an empty Digests with an empty DigestError is a legitimately
	// absent metadata set (built locally, docker load'd), not a failed
	// probe.
	DigestError string `json:"digest_error,omitempty"`
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
	// Platform is the effective target this row's registry metadata was read
	// at: the platform Compose declares for the service, or the default when
	// it declares none. Every field below describing the image itself --
	// Version, Revision, Created, RegistryCreated, RegistryRevision -- came
	// from the child of a multi-platform index serving this platform, so a
	// reader cannot check one without knowing which. Empty only when the
	// declared platform could not be parsed and nothing was inspected.
	Platform string `json:"platform,omitempty"`
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
	// RunningError is set when the daemon's socket was present but the
	// required running-container check failed, as on every row of that
	// run. It keeps a row with no independent finding from reading as
	// "current", and it lets a row that does have an independent finding
	// say "update-available, but running state unverified" rather than
	// claiming the deployment was checked. Absent when the socket itself
	// was missing: an absent optional socket is a skipped check, not a
	// failed one, so it carries no RunningError.
	RunningError string `json:"running_error,omitempty"`
}

// StackFailure is one stack that was skipped or partially skipped, with the
// reason. It is the structured counterpart of the "warning:" lines printed
// to stderr.
type StackFailure struct {
	Stack  string `json:"stack"`
	Reason string `json:"reason"`
}

// Diagnostics names what the run did and did not manage to check, so a JSON
// consumer never has to scrape stderr. The counts have documented meanings:
// StacksChecked is the stacks that parsed and fed the report; StacksSkipped
// is the enrolled stacks that produced nothing, because their path vanished
// or the compose file would not parse. ServicesChecked is the rows in
// Images; ServicesSkipped is the services dropped on per-service warnings.
// An empty inventory has every count zero and empty lists; a run where
// every enrolled stack failed has StacksChecked zero, StacksSkipped equal
// to the enrolled count, and the reasons in the lists.
type Diagnostics struct {
	MissingStacks   []StackFailure `json:"missing_stacks,omitempty"`
	ParseFailures   []StackFailure `json:"parse_failures,omitempty"`
	ServiceWarnings []StackFailure `json:"service_warnings,omitempty"`
	StacksChecked   int            `json:"stacks_checked"`
	StacksSkipped   int            `json:"stacks_skipped"`
	ServicesChecked int            `json:"services_checked"`
	ServicesSkipped int            `json:"services_skipped"`
}

// Report is the whole run.
type Report struct {
	Generated time.Time `json:"generated"`
	// DockerAvailable is false when the socket was absent, in which case the
	// running-container signal is missing from every row.
	DockerAvailable bool `json:"docker_available"`
	// DockerError is set when the socket was present but the container
	// listing failed. docker_available + docker_error together distinguish
	// the three states: true/empty means the daemon answered;
	// false/empty means the optional socket was absent (file/Registry-only
	// behavior); false/set means a socket existed but the probe failed, and
	// rows were not verified against running state.
	DockerError string  `json:"docker_error,omitempty"`
	Images      []Image `json:"images"`
	// Diagnostics is always present, empty included, so a consumer reads
	// the same keys on every report.
	Diagnostics Diagnostics `json:"diagnostics"`
}

// Incomplete reports whether any required probe or required parse/inspection
// step failed or was skipped. An absent optional Docker socket does not
// count, nor does a replica that legitimately has no recorded digests (a
// locally built image): only actual failures and skipped required steps do.
func (r Report) Incomplete() bool {
	if r.DockerError != "" ||
		len(r.Diagnostics.MissingStacks) > 0 ||
		len(r.Diagnostics.ParseFailures) > 0 ||
		len(r.Diagnostics.ServiceWarnings) > 0 {
		return true
	}
	for _, i := range r.Images {
		if i.Err != "" || i.RunningError != "" || i.IdentityNote != "" {
			return true
		}
		for _, rep := range i.Replicas {
			if rep.DigestError != "" {
				return true
			}
		}
	}
	return false
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
	// A failed required running-state check never becomes "current" or
	// "not-running": without the running-container signal there is nothing
	// behind those verdicts. Independently established findings above
	// (update, drift) still apply, so the row keeps them and marks the
	// comparison incomplete rather than pretending it was verified.
	if i.RunningError != "" {
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
		if i.RunningError != "" {
			out = append(out, StatusUnknown)
		} else {
			out = append(out, StatusCurrent)
		}
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
