// Package health assembles runtime container liveness and Docker healthcheck
// verdicts for enrolled Compose stacks. It is deliberately separate from
// package report: runtime health is independent of image freshness and never
// contacts a container registry.
package health

import "time"

// ContainerStatus is a container's liveness / lifecycle state. It is a
// distinct vocabulary from report.Status (which is about image versions and
// digests).
type ContainerStatus string

const (
	StatusRunning    ContainerStatus = "running"
	StatusExited     ContainerStatus = "exited"
	StatusCreated    ContainerStatus = "created"
	StatusRestarting ContainerStatus = "restarting"
	StatusPaused     ContainerStatus = "paused"
	StatusRemoving   ContainerStatus = "removing"
	StatusDead       ContainerStatus = "dead"
	StatusAbsent     ContainerStatus = "absent"
	StatusUnknown    ContainerStatus = "unknown"
)

// CheckStatus is the Docker healthcheck verdict for a container.
// HealthNone is emitted only when a container was inspected and has no
// healthcheck configured (or is confirmed absent); HealthUnknown is used when
// the daemon or container inspection was unavailable.
type CheckStatus string

const (
	HealthHealthy   CheckStatus = "healthy"
	HealthUnhealthy CheckStatus = "unhealthy"
	HealthStarting  CheckStatus = "starting"
	HealthNone      CheckStatus = "none"
	HealthUnknown   CheckStatus = "unknown"
)

// Replica is one inspected container instance of a Service.
type Replica struct {
	ID     string          `json:"id,omitempty"`
	Name   string          `json:"name"`
	Status ContainerStatus `json:"status"`
	Health CheckStatus     `json:"health"`
	Err    string          `json:"error,omitempty"`
}

// Service is the runtime health verdict for one declared Service of an
// enrolled Stack. When multiple Replicas exist, Container, Status, and Health
// report the worst Replica (with lexical container-name tie-break).
type Service struct {
	Stack        string          `json:"stack"`
	Service      string          `json:"service"`
	Project      string          `json:"project,omitempty"`
	ProjectBound bool            `json:"project_bound"`
	Container    string          `json:"container"`
	Status       ContainerStatus `json:"status"`
	Health       CheckStatus     `json:"health"`
	Replicas     []Replica       `json:"replicas,omitempty"`
	ReplicaCount int             `json:"replicas_count"`
	Disagreeing  int             `json:"disagreeing_replicas"`
	ReplicaNote  string          `json:"replica_detail,omitempty"`
	Err          string          `json:"error,omitempty"`
	IdentityNote string          `json:"identity_note,omitempty"`
}

// StackFailure records an enrolled Stack whose Compose file was missing or
// could not be parsed, so the Stack remains visible as a structured failure
// rather than vanishing or inventing fake per-Service rows.
type StackFailure struct {
	Stack  string `json:"stack"`
	Reason string `json:"reason"`
}

// Report is the runtime health result for one run.
type Report struct {
	Generated       time.Time      `json:"generated"`
	DockerAvailable bool           `json:"docker_available"`
	DockerError     string         `json:"docker_error,omitempty"`
	Services        []Service      `json:"services"`
	StackFailures   []StackFailure `json:"stack_failures"`
}

// Unhealthy reports whether the run has any condition that must fail
// --fail-on-unhealthy (exit 2): an unavailable or failing Docker daemon,
// missing or unparseable Stacks, unresolved Project identity, or any Service
// or Replica that is not confirmed running with health healthy or none.
func (r Report) Unhealthy() bool {
	if !r.DockerAvailable || r.DockerError != "" || len(r.StackFailures) > 0 {
		return true
	}
	for _, s := range r.Services {
		if !isGood(s.Status, s.Health) || s.Err != "" || s.IdentityNote != "" {
			return true
		}
		for _, rep := range s.Replicas {
			if !isGood(rep.Status, rep.Health) || rep.Err != "" {
				return true
			}
		}
	}
	return false
}

func isGood(status ContainerStatus, check CheckStatus) bool {
	return status == StatusRunning && (check == HealthHealthy || check == HealthNone)
}
