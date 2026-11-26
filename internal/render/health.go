package render

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/gvwalker/stackmon/internal/health"
)

// CompactHealth is the stable JSON shape emitted by `stackmon health --compact`.
// Every field is always present on the top-level document and on every service
// row so consumers never have to distinguish an omitted field from an empty or
// false value.
type CompactHealth struct {
	DockerAvailable bool                   `json:"docker_available"`
	DockerError     string                 `json:"docker_error"`
	Services        []CompactHealthService `json:"services"`
	StackFailures   []health.StackFailure  `json:"stack_failures"`
}

// CompactHealthService is one declared Service's runtime health row.
type CompactHealthService struct {
	Stack               string                 `json:"stack"`
	Service             string                 `json:"service"`
	Container           string                 `json:"container"`
	Status              health.ContainerStatus `json:"status"`
	Health              health.CheckStatus     `json:"health"`
	Replicas            int                    `json:"replicas"`
	DisagreeingReplicas int                    `json:"disagreeing_replicas"`
	ReplicaDetail       string                 `json:"replica_detail"`
	Err                 string                 `json:"error"`
	IdentityNote        string                 `json:"identity_note"`
}

// CompactHealthJSON writes the runtime health report in its compact JSON form.
func CompactHealthJSON(w io.Writer, r health.Report) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(compactHealth(r))
}

func compactHealth(r health.Report) CompactHealth {
	out := CompactHealth{
		DockerAvailable: r.DockerAvailable,
		DockerError:     r.DockerError,
		Services:        make([]CompactHealthService, 0, len(r.Services)),
		StackFailures:   make([]health.StackFailure, 0, len(r.StackFailures)),
	}
	for _, s := range r.Services {
		out.Services = append(out.Services, CompactHealthService{
			Stack:               s.Stack,
			Service:             s.Service,
			Container:           s.Container,
			Status:              s.Status,
			Health:              s.Health,
			Replicas:            s.ReplicaCount,
			DisagreeingReplicas: s.Disagreeing,
			ReplicaDetail:       s.ReplicaNote,
			Err:                 s.Err,
			IdentityNote:        s.IdentityNote,
		})
	}
	out.StackFailures = append(out.StackFailures, r.StackFailures...)
	return out
}

// HealthTable writes a human-readable runtime health table, one row per
// declared Service.
func HealthTable(w io.Writer, r health.Report) error {
	if !r.DockerAvailable {
		if r.DockerError != "" {
			if _, err := fmt.Fprintf(w, "note: docker socket present but container listing failed: %s; runtime health was not verified\n", r.DockerError); err != nil {
				return err
			}
		} else if _, err := fmt.Fprintln(w, "note: docker socket unavailable; runtime health was not checked"); err != nil {
			return err
		}
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "STACK\tSERVICE\tCONTAINER\tSTATUS\tHEALTH\tDETAIL"); err != nil {
		return err
	}
	for _, s := range r.Services {
		container := s.Container
		if container == "" {
			container = "-"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			s.Stack, s.Service, container, s.Status, s.Health, healthDetailCell(s)); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func healthDetailCell(s health.Service) string {
	var parts []string
	if s.Status == health.StatusAbsent {
		parts = append(parts, "no container found")
	}
	if s.ReplicaNote != "" {
		parts = append(parts, s.ReplicaNote)
	} else if s.ReplicaCount > 1 {
		parts = append(parts, fmt.Sprintf("%d replicas", s.ReplicaCount))
	}
	if reason := firstNonEmpty(s.Err, s.IdentityNote); reason != "" {
		parts = append(parts, reason)
	}
	return strings.Join(parts, "; ")
}
