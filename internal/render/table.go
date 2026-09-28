// Package render turns a report into human-readable output. It consumes
// package report and nothing else, so that a TUI or web view can be added
// later as an additive consumer.
package render

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/gvwalker/stackmon/internal/report"
)

// Table writes one row per image.
func Table(w io.Writer, r report.Report) error {
	if !r.DockerAvailable {
		if _, err := fmt.Fprintln(w, "note: docker socket unavailable; running-container state was not checked"); err != nil {
			return err
		}
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "STACK\tSERVICE\tVERSION\tSTATUS\tDETAIL"); err != nil {
		return err
	}

	for _, i := range r.Images {
		version := i.Version
		if version == "" {
			version = "-"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", i.Stack, i.Service, version, i.Status, detailCell(i)); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// detailCell is the one-line explanation shown beside a status.
func detailCell(i report.Image) string {
	// A service is not one container, so how many of its replicas agree, and
	// against which digest, is the fact behind most verdicts.
	note := replicaNote(i.ReplicaComparison)

	var cell string
	switch i.Status {
	case report.StatusUnknown:
		// Three different reasons to say nothing definite: a failed probe,
		// a project identity that could not be established, and a replica
		// with no digest to judge.
		cell = firstNonEmpty(i.Err, i.IdentityNote, note)
		return cell
	case report.StatusUpdateAvailable:
		if i.KindName != "" {
			cell = fmt.Sprintf("%s available (%s)", i.Candidate, i.KindName)
		} else {
			cell = i.Candidate + " available"
		}
	case report.StatusDigestDrift:
		cell = "same tag, new digest upstream"
	case report.StatusNotDeployed:
		cell = "declared pin not yet deployed"
	case report.StatusStaleDeployment:
		cell = "running image older than the tag now resolves to"
	case report.StatusNotRunning:
		cell = "no running container"
	case report.StatusCurrent:
		// All the replicas agreed, which is only worth saying when there was
		// more than one of them.
		if len(i.Replicas) < 2 {
			return ""
		}
		return note
	default:
		return ""
	}

	if note != "" {
		if cell == "" {
			return note
		}
		cell += "; " + note
	}
	return cell
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// replicaNote summarises a service's replicas in one line: how many agree
// with the compared digest, how many differ, how many could not be judged.
func replicaNote(c report.ReplicaComparison) string {
	if c.Matching == 0 && c.Mismatching == 0 && c.Unknown == 0 {
		return ""
	}
	against := "no digest to compare against"
	if c.Compared != "" {
		against = shortDigest(c.Compared)
	}

	var parts []string
	if c.Matching > 0 {
		parts = append(parts, fmt.Sprintf("%d match %s", c.Matching, against))
	}
	if c.Mismatching > 0 {
		parts = append(parts, fmt.Sprintf("%d differ from %s", c.Mismatching, against))
	}
	if c.Unknown > 0 {
		parts = append(parts, fmt.Sprintf("%d unknown, no digest recorded", c.Unknown))
	}
	return fmt.Sprintf("of %d replicas, %s", c.Matching+c.Mismatching+c.Unknown, strings.Join(parts, ", "))
}

// shortDigest keeps a digest recognisable in a table cell: the algorithm plus
// the first twelve hex characters say as much as all sixty-four do, without
// taking the row's width away from the status.
func shortDigest(digest string) string {
	algo, hex, found := strings.Cut(digest, ":")
	if !found {
		return digest
	}
	if len(hex) > 12 {
		hex = hex[:12]
	}
	return algo + ":" + hex
}
