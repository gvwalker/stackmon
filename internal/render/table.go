// Package render turns a report into human-readable output. It consumes
// package report and nothing else, so that a TUI or web view can be added
// later as an additive consumer.
package render

import (
	"fmt"
	"io"
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
	switch i.Status {
	case report.StatusUnknown:
		return i.Err
	case report.StatusUpdateAvailable:
		if i.KindName != "" {
			return fmt.Sprintf("%s available (%s)", i.Candidate, i.KindName)
		}
		return i.Candidate + " available"
	case report.StatusDigestDrift:
		return "same tag, new digest upstream"
	case report.StatusNotDeployed:
		return "declared pin not yet deployed"
	case report.StatusStaleDeployment:
		return "running image older than the tag now resolves to"
	case report.StatusNotRunning:
		return "no running container"
	default:
		return ""
	}
}
