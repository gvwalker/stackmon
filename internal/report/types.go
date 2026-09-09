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

// Image is one service's image and everything learned about it.
type Image struct {
	Stack   string       `json:"stack"`
	Service string       `json:"service"`
	Ref     imageref.Ref `json:"ref"`

	// DeclaredDigest is the digest written in the compose file, empty for a
	// tag-only reference.
	DeclaredDigest string `json:"declared_digest,omitempty"`
	// RunningDigest is the digest the container is actually using.
	RunningDigest string `json:"running_digest,omitempty"`
	// RegistryDigest is what the registry serves for the declared reference.
	RegistryDigest string `json:"registry_digest,omitempty"`
	// DockerChecked records whether the daemon was reachable, so that an
	// absent running digest is not mistaken for a stopped container.
	DockerChecked bool `json:"docker_checked"`

	// Version is the current version: the tag, or for a digest-only pin the
	// org.opencontainers.image.version label.
	Version string `json:"version,omitempty"`
	// Candidate is the newest tag that supersedes Version.
	Candidate string      `json:"candidate,omitempty"`
	Kind      policy.Kind `json:"-"`
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
	if i.DeclaredDigest != "" && i.RunningDigest != "" && i.DeclaredDigest != i.RunningDigest {
		out = append(out, StatusNotDeployed)
	}
	// A floating reference has no declared digest, so a running container that
	// differs from the registry means the pull is stale.
	if i.DeclaredDigest == "" && i.RunningDigest != "" && i.RegistryDigest != "" && i.RunningDigest != i.RegistryDigest {
		out = append(out, StatusStaleDeployment)
	}
	if i.DockerChecked && i.RunningDigest == "" {
		out = append(out, StatusNotRunning)
	}
	if len(out) == 0 {
		out = append(out, StatusCurrent)
	}
	return out
}

// Decide returns the headline status: the most actionable one that applies.
func Decide(i Image) Status {
	return applicable(i)[0]
}
