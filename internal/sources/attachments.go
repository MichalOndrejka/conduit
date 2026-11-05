// Attachment metadata. Files attached to work items, test results, test
// runs and pull requests, and the artifacts pipeline runs publish, are never
// downloaded — they can be zips of many gigabytes. Only what the listing
// APIs return (name, size, type, uploader, date, link) is embedded, with a
// note telling the agent to ask the user for a file it needs.
package sources

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// attachmentNote follows every attachment list, so the agent knows the
// contents aren't searchable and how to get a file.
const attachmentNote = "metadata only: the files themselves are not indexed. " +
	"If one matters, ask the user to download it from the link and provide it."

// attachmentInfo is the metadata of one attached file or published artifact.
type attachmentInfo struct {
	Name    string
	Kind    string // attachment or artifact type, e.g. "GeneralAttachment", "PipelineArtifact"
	Size    int64  // bytes; < 0 when the API doesn't say
	By      string // uploader
	Created string
	Comment string
	URL     string
	Note    string // e.g. "expired"
}

// line renders the attachment as one list entry.
func (a attachmentInfo) line() string {
	var meta []string
	if a.Size >= 0 {
		meta = append(meta, formatSize(a.Size))
	}
	if a.Kind != "" {
		meta = append(meta, a.Kind)
	}
	if a.By != "" {
		meta = append(meta, "by "+a.By)
	}
	if a.Created != "" {
		meta = append(meta, a.Created)
	}
	if a.Note != "" {
		meta = append(meta, a.Note)
	}
	s := orDefault(strings.TrimSpace(a.Name), "(unnamed)")
	if len(meta) > 0 {
		s += " (" + strings.Join(meta, ", ") + ")"
	}
	if c := strings.Join(strings.Fields(a.Comment), " "); c != "" {
		s += " — " + c
	}
	if a.URL != "" {
		s += ": " + a.URL
	}
	return s
}

// writeAttachments appends a "<heading> (n), metadata only…" section; notes
// say why a list couldn't be read. Nothing is written when both are empty.
func writeAttachments(sb *strings.Builder, heading string, atts []attachmentInfo, notes ...string) {
	if len(atts) == 0 && len(notes) == 0 {
		return
	}
	fmt.Fprintf(sb, "\n%s (%d), %s\n", heading, len(atts), attachmentNote)
	for _, a := range atts {
		sb.WriteString("- " + a.line() + "\n")
	}
	for _, n := range notes {
		sb.WriteString("- " + n + "\n")
	}
}

// formatSize renders a byte count the way a person reads it ("8.4 GB").
func formatSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTP"[exp])
}

// listUnavailable turns a 404 from an attachment or artifact listing into a
// note for the document — older servers lack some of these APIs, and the
// rest of the item is still worth indexing. Any other error is returned.
func listUnavailable(what string, err error) (string, error) {
	var se *httpStatusError
	if errors.As(err, &se) && se.status == http.StatusNotFound {
		return fmt.Sprintf("(%s couldn't be listed: the server answered HTTP 404)", what), nil
	}
	return "", err
}

// previewVersion appends -preview.1 to an api-version that lacks a preview
// suffix, for APIs Azure DevOps only offers in preview.
func previewVersion(v string) string {
	if strings.Contains(v, "preview") {
		return v
	}
	return v + "-preview.1"
}

// anyInt64 reads a JSON number (or numeric string) from a decoded map value;
// -1 when it is neither.
func anyInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case string:
		var i int64
		if _, err := fmt.Sscan(n, &i); err == nil {
			return i
		}
	}
	return -1
}
