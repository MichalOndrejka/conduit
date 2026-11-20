// Git refs (branches and tags) a repository-backed source embeds: Source
// Code, Test Code, Documentation, Requirements in files mode and Commit
// History. A source may list several, to embed several versions of the
// software side by side:
//
//	Branch  comma-separated branch names
//	Tags    comma-separated tag names
//
// Neither set means the repository's default branch. Every document carries
// the refs it was found in as its version filter (models.FilterVersion), and
// what is the same in several refs is embedded once: a file with the same
// path and content, a commit reachable from several of them.
package sources

import (
	"fmt"
	"sort"
	"strings"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// gitRef is a branch or a tag.
type gitRef struct {
	Name string
	Tag  bool
}

// configuredRefs returns src's branches, then its tags, in the order given,
// without repeats; nil when none are set (the default branch).
func configuredRefs(src *models.SourceDefinition) []gitRef {
	var refs []gitRef
	seen := map[gitRef]bool{}
	add := func(raw string, tag bool) {
		for _, name := range strings.Split(raw, ",") {
			name = strings.TrimSpace(name)
			if tag {
				name = strings.TrimPrefix(name, "refs/tags/")
			} else {
				name = strings.TrimPrefix(name, "refs/heads/")
			}
			r := gitRef{Name: name, Tag: tag}
			if name != "" && !seen[r] {
				seen[r] = true
				refs = append(refs, r)
			}
		}
	}
	add(src.GetConfig("Branch"), false)
	add(src.GetConfig("Tags"), true)
	return refs
}

// refNames returns the refs' names, comma-separated for a document property.
func refNames(refs []gitRef) string {
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = r.Name
	}
	return strings.Join(names, ", ")
}

// addVersionFilters stores refs as the document's version filter and
// property.
func addVersionFilters(f *models.DocumentFilters, props map[string]string, refs []gitRef) {
	for _, r := range refs {
		f.Add(models.FilterVersion, r.Name)
	}
	if len(refs) > 0 {
		props["version"] = refNames(refs)
	}
}

// refFile is one file version: a path and content found in one or more refs.
type refFile struct {
	archiveFile
	refs []gitRef // the refs holding exactly this content, in source order
	// variant is set when the path has different content in other refs, so
	// its document ID and title need the ref to be told apart.
	variant bool
}

// mergeRefFiles merges the files read from each ref (perRef[i] from refs[i])
// into one entry per path and content, listing every ref that holds it, sorted
// by path and then by the first ref holding each version.
func mergeRefFiles(refs []gitRef, perRef [][]archiveFile) []refFile {
	type key struct {
		path, text string
		skipped    bool
	}
	index := map[key]int{}
	var out []refFile
	for i, files := range perRef {
		for _, f := range files {
			k := key{f.path, f.text, f.skipped}
			if j, ok := index[k]; ok {
				out[j].refs = append(out[j].refs, refs[i])
				continue
			}
			index[k] = len(out)
			out = append(out, refFile{archiveFile: f, refs: []gitRef{refs[i]}})
		}
	}
	variants := map[string]int{}
	for _, f := range out {
		variants[f.path]++
	}
	for i := range out {
		out[i].variant = variants[out[i].path] > 1
	}
	order := make(map[gitRef]int, len(refs))
	for i, r := range refs {
		order[r] = i
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].path != out[j].path {
			return out[i].path < out[j].path
		}
		return order[out[i].refs[0]] < order[out[j].refs[0]]
	})
	return out
}

// docKey is the part of a file document's ID after the source: its path, and
// for one of several versions of the path also the first ref holding it.
func (f refFile) docKey() string {
	if !f.variant {
		return f.path
	}
	return f.path + "@" + f.refs[0].Name
}

// title is the file's path, followed by its refs when other refs hold the
// path with different content.
func (f refFile) title() string {
	if !f.variant {
		return f.path
	}
	return fmt.Sprintf("%s (%s)", f.path, refNames(f.refs))
}

// refCommits collects the commits listed from each ref, keeping each once in
// the order first listed and every ref it was listed from.
type refCommits[T any] struct {
	items []T
	refs  [][]gitRef
	index map[string]int
}

func (c *refCommits[T]) add(id string, item T, ref gitRef) {
	if c.index == nil {
		c.index = map[string]int{}
	}
	if i, ok := c.index[id]; ok {
		c.refs[i] = append(c.refs[i], ref)
		return
	}
	c.index[id] = len(c.items)
	c.items = append(c.items, item)
	c.refs = append(c.refs, []gitRef{ref})
}
