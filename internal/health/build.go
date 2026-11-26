package health

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gvwalker/stackmon/internal/compose"
	"github.com/gvwalker/stackmon/internal/local"
)

// Options configures a runtime health run.
type Options struct {
	Docker *local.Client
}

// Build probes Docker for all containers (running and stopped) and assembles
// deterministic runtime health rows for every declared Service across stacks.
// It never contacts any container registry.
func Build(ctx context.Context, stacks []compose.Stack, opts Options) Report {
	r := Report{
		Generated:     time.Now().UTC(),
		Services:      []Service{},
		StackFailures: []StackFailure{},
	}

	var containers []local.Container
	if opts.Docker != nil && opts.Docker.Available() {
		cs, err := opts.Docker.AllContainers(ctx)
		if err == nil {
			r.DockerAvailable = true
			containers = cs
		} else {
			r.DockerError = err.Error()
		}
	}

	indexed := local.Index(containers)

	for _, st := range stacks {
		if !r.DockerAvailable {
			reason := r.DockerError
			if reason == "" {
				reason = "docker socket unavailable"
			}
			for _, svcName := range st.ServiceNames {
				r.Services = append(r.Services, Service{
					Stack:   st.Name,
					Service: svcName,
					Status:  StatusUnknown,
					Health:  HealthUnknown,
					Err:     reason,
				})
			}
			continue
		}

		id := local.ResolveProject(containers, st.Project, st.Dir, st.Path())
		if !id.Resolved && id.Reason != "" {
			note := id.Note(st.Name, st.Path())
			for _, svcName := range st.ServiceNames {
				r.Services = append(r.Services, Service{
					Stack:        st.Name,
					Service:      svcName,
					Status:       StatusUnknown,
					Health:       HealthUnknown,
					IdentityNote: note,
				})
			}
			continue
		}

		for _, svcName := range st.ServiceNames {
			var cs []local.Container
			if id.Project != "" {
				cs = indexed[id.Project+"/"+svcName]
			}
			r.Services = append(r.Services, assembleService(st.Name, svcName, id, cs))
		}
	}

	sort.Slice(r.Services, func(i, j int) bool {
		if r.Services[i].Stack != r.Services[j].Stack {
			return r.Services[i].Stack < r.Services[j].Stack
		}
		return r.Services[i].Service < r.Services[j].Service
	})
	return r
}

func assembleService(stack, svcName string, id local.Identity, cs []local.Container) Service {
	svc := Service{
		Stack:        stack,
		Service:      svcName,
		Project:      id.Project,
		ProjectBound: id.Bound,
	}
	if len(cs) == 0 {
		svc.Status = StatusAbsent
		svc.Health = HealthNone
		return svc
	}

	replicas := make([]Replica, 0, len(cs))
	for _, c := range cs {
		replicas = append(replicas, toReplica(c))
	}
	sort.Slice(replicas, func(i, j int) bool {
		return replicaWorse(replicas[i], replicas[j])
	})

	worst := replicas[0]
	disagreeing := 0
	for _, rep := range replicas[1:] {
		if rep.Status != worst.Status || rep.Health != worst.Health {
			disagreeing++
		}
	}

	containerName := worst.Name
	if containerName == "" {
		containerName = worst.ID
	}

	svc.Container = containerName
	svc.Status = worst.Status
	svc.Health = worst.Health
	svc.Replicas = replicas
	svc.ReplicaCount = len(replicas)
	svc.Disagreeing = disagreeing
	if disagreeing > 0 {
		svc.ReplicaNote = formatReplicaDetail(replicas)
	}
	svc.Err = worst.Err
	if svc.Err == "" {
		var otherErrs []string
		for _, rep := range replicas[1:] {
			if rep.Err != "" {
				label := rep.Name
				if label == "" {
					label = rep.ID
				}
				otherErrs = append(otherErrs, fmt.Sprintf("replica %s: %s", label, rep.Err))
			}
		}
		svc.Err = strings.Join(otherErrs, "; ")
	}
	return svc
}

func toReplica(c local.Container) Replica {
	name := c.Name
	if name == "" {
		name = c.ID
	}
	if c.InspectError != "" {
		return Replica{
			ID:     c.ID,
			Name:   name,
			Status: StatusUnknown,
			Health: HealthUnknown,
			Err:    c.InspectError,
		}
	}

	var errs []string
	st, ok := parseContainerStatus(c.State)
	if !ok {
		st = StatusUnknown
		errs = append(errs, fmt.Sprintf("unrecognized container state %q", c.State))
	}
	hl, ok := parseCheckStatus(c.Health)
	if !ok {
		hl = HealthUnknown
		errs = append(errs, fmt.Sprintf("unrecognized container health %q", c.Health))
	}
	return Replica{
		ID:     c.ID,
		Name:   name,
		Status: st,
		Health: hl,
		Err:    strings.Join(errs, "; "),
	}
}

func parseContainerStatus(raw string) (ContainerStatus, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "running":
		return StatusRunning, true
	case "exited":
		return StatusExited, true
	case "created":
		return StatusCreated, true
	case "restarting":
		return StatusRestarting, true
	case "paused":
		return StatusPaused, true
	case "removing":
		return StatusRemoving, true
	case "dead":
		return StatusDead, true
	default:
		return StatusUnknown, false
	}
}

func parseCheckStatus(raw string) (CheckStatus, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "healthy":
		return HealthHealthy, true
	case "unhealthy":
		return HealthUnhealthy, true
	case "starting":
		return HealthStarting, true
	case "none":
		return HealthNone, true
	default:
		return HealthUnknown, false
	}
}

// severityRank orders container verdicts from worst (lowest number) to best so
// the most actionable fault always surfaces first. Known faults outrank
// unknown/uninspected states, which outrank starting, which outrank known good.
func severityRank(st ContainerStatus, hl CheckStatus) int {
	switch st {
	case StatusDead:
		return 1
	case StatusExited:
		return 2
	case StatusRestarting:
		return 3
	case StatusRunning:
		switch hl {
		case HealthUnhealthy:
			return 4
		case HealthUnknown:
			return 8
		case HealthStarting:
			return 9
		case HealthNone:
			return 10
		case HealthHealthy:
			return 11
		default:
			return 8
		}
	case StatusPaused:
		return 5
	case StatusCreated:
		return 6
	case StatusRemoving:
		return 7
	case StatusUnknown:
		return 8
	case StatusAbsent:
		return 12
	default:
		return 8
	}
}

func healthRank(hl CheckStatus) int {
	switch hl {
	case HealthUnhealthy:
		return 1
	case HealthUnknown:
		return 2
	case HealthStarting:
		return 3
	case HealthNone:
		return 4
	case HealthHealthy:
		return 5
	default:
		return 2
	}
}

// replicaWorse reports whether a is strictly worse than b, breaking ties
// lexically by container name and then ID for deterministic output.
func replicaWorse(a, b Replica) bool {
	if ra, rb := severityRank(a.Status, a.Health), severityRank(b.Status, b.Health); ra != rb {
		return ra < rb
	}
	if ha, hb := healthRank(a.Health), healthRank(b.Health); ha != hb {
		return ha < hb
	}
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.ID < b.ID
}

// formatReplicaDetail summarizes disagreeing replicas in worst-first order so
// both table and compact JSON outputs are stable and self-explanatory.
func formatReplicaDetail(sorted []Replica) string {
	type bucket struct {
		status ContainerStatus
		health CheckStatus
		count  int
	}
	var buckets []bucket
	for _, rep := range sorted {
		if n := len(buckets); n > 0 && buckets[n-1].status == rep.Status && buckets[n-1].health == rep.Health {
			buckets[n-1].count++
			continue
		}
		buckets = append(buckets, bucket{status: rep.Status, health: rep.Health, count: 1})
	}
	parts := make([]string, 0, len(buckets))
	for _, b := range buckets {
		parts = append(parts, fmt.Sprintf("%d %s/%s", b.count, b.status, b.health))
	}
	return fmt.Sprintf("of %d replicas: %s", len(sorted), strings.Join(parts, ", "))
}
