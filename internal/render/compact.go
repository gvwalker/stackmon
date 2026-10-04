package render

import (
	"encoding/json"
	"io"

	"github.com/gvwalker/stackmon/internal/report"
)

// Compact is the report reduced to what a script has to read: whether the
// running state was established at all, and for each service the verdict, what
// is running now, and what would supersede it. The full report answers
// "why"; this one answers "what should I do", which is why it is a projection
// rather than a filtered table -- a field is here because a reader acting on
// it would be stuck without it, and absent otherwise.
type Compact struct {
	// DockerAvailable is top-level because it qualifies every row below: when
	// the socket is down, a row saying "current" is a claim nothing checked,
	// not a claim everything is fine.
	DockerAvailable bool           `json:"docker_available"`
	Images          []CompactImage `json:"images"`
}

// CompactImage is one service's row. Every field is always present, empty
// included, so a consumer reads the same keys on every row and never has to
// ask whether a missing field means "false" or "not applicable".
type CompactImage struct {
	// Stack and Service identify the row: services are only named within
	// their stack, and a digest verdict is per service.
	Stack   string `json:"stack"`
	Service string `json:"service"`
	// Status is the verdict, and the reason the other fields are here.
	Status report.Status `json:"status"`
	// Version is what is running now, empty when there is no version to name.
	Version string `json:"version"`
	// Candidate is the tag that supersedes Version, empty when none does.
	// A candidate is what turns update-available from a nag into an action.
	Candidate string `json:"candidate"`
	// Err explains a status of unknown, and is empty for every other status:
	// it is the reason, and there is no reason when the verdict stands.
	Err string `json:"error"`
	// IdentityNote is the other reason for unknown -- a Compose project that
	// could not be resolved -- and is empty in the same way.
	IdentityNote string `json:"identity_note"`
}

// CompactJSON writes the report in its compact JSON form. HTML escaping is
// off: this document is read by a shell and by jq, and \u003cproject\u003e is
// neither shorter nor easier to match than <project>.
func CompactJSON(w io.Writer, r report.Report) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(compact(r))
}

// compact projects the report. It never consults Docker or the registry: the
// projection is the same set of fields whatever produced the report, so a
// stack that degraded to unknown is still a row.
func compact(r report.Report) Compact {
	out := Compact{
		DockerAvailable: r.DockerAvailable,
		Images:          make([]CompactImage, 0, len(r.Images)),
	}
	for _, i := range r.Images {
		out.Images = append(out.Images, CompactImage{
			Stack:        i.Stack,
			Service:      i.Service,
			Status:       i.Status,
			Version:      i.Version,
			Candidate:    i.Candidate,
			Err:          i.Err,
			IdentityNote: i.IdentityNote,
		})
	}
	return out
}
