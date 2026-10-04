package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/gvwalker/stackmon/internal/notes"
	"github.com/gvwalker/stackmon/internal/report"
)

// Detail writes the per-image view: how the image changed, and what the
// release notes say.
func Detail(w io.Writer, i report.Image, rels []notes.Release) error {
	if _, err := fmt.Fprintf(w, "%s / %s\n", i.Stack, i.Service); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  reference: %s\n", i.Ref.Resolved); err != nil {
		return err
	}
	if i.Ref.Interpolated {
		if _, err := fmt.Fprintf(w, "  declared:  %s\n", i.Ref.Raw); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "  status:    %s\n", i.Status); err != nil {
		return err
	}

	if i.Version != "" {
		if _, err := fmt.Fprintf(w, "  version:   %s\n", i.Version); err != nil {
			return err
		}
	}
	if i.Candidate != "" {
		available := i.Candidate
		if i.KindName != "" {
			available = fmt.Sprintf("%s (%s)", i.Candidate, i.KindName)
		}
		if _, err := fmt.Fprintf(w, "  available: %s\n", available); err != nil {
			return err
		}
	}
	if i.Err != "" {
		if _, err := fmt.Fprintf(w, "  error:     %s\n", i.Err); err != nil {
			return err
		}
	}
	if i.RunningError != "" {
		if _, err := fmt.Fprintf(w, "  running:   %s\n", i.RunningError); err != nil {
			return err
		}
	}
	if i.IdentityNote != "" {
		if _, err := fmt.Fprintf(w, "  note:      %s\n", i.IdentityNote); err != nil {
			return err
		}
	}

	if err := writeReplicas(w, i); err != nil {
		return err
	}
	if err := writeDigestChange(w, i); err != nil {
		return err
	}
	return writeNotes(w, i, rels)
}

// writeReplicas lists every running container of the service and how each one
// compared. A service is not one container: showing a single image's digests
// would hide a replica still running yesterday's build.
func writeReplicas(w io.Writer, i report.Image) error {
	if !i.DockerChecked {
		return nil
	}
	if i.Project != "" {
		from := "matched from its compose file"
		if i.ProjectBound {
			from = "bound by enrollment"
		}
		if _, err := fmt.Fprintf(w, "  project:   %s (%s)\n", i.Project, from); err != nil {
			return err
		}
	}
	if len(i.Replicas) == 0 {
		return nil
	}

	if _, err := fmt.Fprintf(w, "\n  replicas (%d)\n", len(i.Replicas)); err != nil {
		return err
	}
	compared := i.ReplicaComparison.Compared
	if compared == "" {
		compared = "(nothing to compare against: no declared or registry digest)"
	} else {
		compared = "compared against " + compared
	}
	if _, err := fmt.Fprintf(w, "    %s\n", compared); err != nil {
		return err
	}

	for _, r := range i.Replicas {
		verdict := "differs"
		switch {
		case len(r.Digests) == 0 && r.DigestError != "":
			verdict = "unknown, digest lookup failed"
		case len(r.Digests) == 0:
			verdict = "unknown, no recorded digest"
		case i.ReplicaComparison.Compared == "":
			verdict = "unknown, nothing to compare against"
		case contains(r.Digests, i.ReplicaComparison.Compared):
			verdict = "match"
		}
		if _, err := fmt.Fprintf(w, "    %s\n", verdict); err != nil {
			return err
		}
		if r.Image != "" {
			if _, err := fmt.Fprintf(w, "      image:   %s\n", r.Image); err != nil {
				return err
			}
		}
		for _, d := range r.Digests {
			if _, err := fmt.Fprintf(w, "      digest:  %s\n", d); err != nil {
				return err
			}
		}
		if r.DigestError != "" {
			if _, err := fmt.Fprintf(w, "      error:   %s\n", r.DigestError); err != nil {
				return err
			}
		}
	}
	return nil
}

func contains(set []string, want string) bool {
	for _, s := range set {
		if s == want {
			return true
		}
	}
	return false
}

// writeDigestChange explains what moved when a digest changed. A digest
// change is never "only a hash": either the source revision moved, or the
// image was rebuilt from the same source.
func writeDigestChange(w io.Writer, i report.Image) error {
	if i.Status != report.StatusDigestDrift {
		return nil
	}

	if _, err := fmt.Fprintf(w, "\n  digest change\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "    declared: %s\n", i.DeclaredDigest); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "    registry: %s\n", i.RegistryDigest); err != nil {
		return err
	}

	if !i.Created.IsZero() && !i.RegistryCreated.IsZero() {
		if _, err := fmt.Fprintf(w, "    built:    %s -> %s\n",
			i.Created.Format("2006-01-02"), i.RegistryCreated.Format("2006-01-02")); err != nil {
			return err
		}
	}

	switch {
	case i.Revision != "" && i.Revision == i.RegistryRevision:
		_, err := fmt.Fprintf(w, "    rebuilt from the same upstream revision (%s): base image or dependency refresh\n", short(i.Revision))
		return err
	case i.Revision != "" && i.RegistryRevision != "":
		_, err := fmt.Fprintf(w, "    upstream revision moved: %s -> %s\n", short(i.Revision), short(i.RegistryRevision))
		return err
	default:
		_, err := fmt.Fprintf(w, "    no revision label, so the cause cannot be determined without pulling\n")
		return err
	}
}

// writeNotes explains what the upstream release notes say for the versions
// between what's declared and what's available. Silence would read as "no
// changes", so an unavailable answer says why.
func writeNotes(w io.Writer, i report.Image, rels []notes.Release) error {
	if i.Candidate == "" {
		return nil
	}

	if _, err := fmt.Fprintf(w, "\n  release notes\n"); err != nil {
		return err
	}
	if len(rels) == 0 {
		reason := "no release notes available"
		if i.NotesRepo == "" {
			reason += " (no source label, and no repo configured for this image)"
		}
		_, err := fmt.Fprintf(w, "    %s\n", reason)
		return err
	}

	for _, r := range rels {
		title := r.Name
		if title == "" {
			title = r.Tag
		}
		if _, err := fmt.Fprintf(w, "\n    %s\n", title); err != nil {
			return err
		}
		for _, line := range strings.Split(strings.TrimSpace(r.Body), "\n") {
			if _, err := fmt.Fprintf(w, "      %s\n", line); err != nil {
				return err
			}
		}
	}
	return nil
}

func short(rev string) string {
	if len(rev) > 8 {
		return rev[:8]
	}
	return rev
}
