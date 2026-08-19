package web

import (
	"sort"
	"strconv"
	"strings"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
)

// docSummary describes one embedded source document (a file, commit, work item,
// requirement, or test case) — everything needed to review "did I embed this"
// without pulling the full chunk text. All fields but ChunkCount come from a
// single representative chunk's payload since doc.Tags/doc.Properties are copied
// identically onto every chunk of a document (see rag.DocumentIndexer.IndexBatch).
type docSummary struct {
	DocID        string
	Title        string
	URL          string
	Type         string
	Area         string
	FilesChanged string
	ChunkCount   int
}

// buildDocSummaries groups scrolled points by their source document, returning
// one summary per document sorted by title.
func buildDocSummaries(points []rag.ScrolledPoint) []docSummary {
	seen := make(map[string]bool, len(points))
	docs := make([]docSummary, 0, len(points))
	for _, p := range points {
		docID, _ := p.Payload[models.PayloadSourceDocID].(string)
		if docID == "" || seen[docID] {
			continue
		}
		seen[docID] = true

		title, _ := p.Payload[models.PropKey("title")].(string)
		if title == "" {
			title = docID
		}
		url, _ := p.Payload[models.PropKey("url")].(string)
		typ, _ := p.Payload[models.PropKey("type")].(string)
		area, _ := p.Payload[models.PropKey("area")].(string)
		filesChanged, _ := p.Payload[models.PropKey("files_changed")].(string)

		count := 1
		if totalStr, ok := p.Payload[models.PayloadTotalChunks].(string); ok {
			if n, err := strconv.Atoi(totalStr); err == nil && n > 0 {
				count = n
			}
		}

		docs = append(docs, docSummary{
			DocID: docID, Title: title, URL: url,
			Type: typ, Area: area, FilesChanged: filesChanged,
			ChunkCount: count,
		})
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Title < docs[j].Title })
	return docs
}

// treeNode is one folder or file in the structure tree built from doc titles
// that look like paths (e.g. code/test-code source-file paths).
type treeNode struct {
	Name     string
	IsLeaf   bool
	Doc      *docSummary
	Children []*treeNode
}

// buildTree turns path-shaped doc titles ("/src/auth/login.go") into a nested
// directory tree, directories sorted before files, alphabetical within each.
func buildTree(docs []docSummary) *treeNode {
	root := &treeNode{}
	for i := range docs {
		d := &docs[i]
		parts := strings.Split(strings.Trim(d.Title, "/"), "/")
		cur := root
		for pi, part := range parts {
			if part == "" {
				continue
			}
			isLast := pi == len(parts)-1
			var child *treeNode
			for _, c := range cur.Children {
				if c.Name == part && c.IsLeaf == isLast {
					child = c
					break
				}
			}
			if child == nil {
				child = &treeNode{Name: part, IsLeaf: isLast}
				cur.Children = append(cur.Children, child)
			}
			if isLast {
				child.Doc = d
			}
			cur = child
		}
	}
	sortTree(root)
	return root
}

func sortTree(n *treeNode) {
	sort.Slice(n.Children, func(i, j int) bool {
		a, b := n.Children[i], n.Children[j]
		if a.IsLeaf != b.IsLeaf {
			return !a.IsLeaf // directories before files
		}
		return a.Name < b.Name
	})
	for _, c := range n.Children {
		sortTree(c)
	}
}

// useTree reports whether the structure panel should render as a folder tree
// (source-file paths) rather than a flat list. collection is the Qdrant
// collection the source writes to (from sources.CollectionFor) — that's what
// actually determines the payload shape, not the raw source Type string, since
// unrecognized/manual types all fall back to the Documentation collection.
func useTree(collection string, docs []docSummary) bool {
	switch collection {
	case models.CollectionCode, models.CollectionTestCode:
		return true
	case models.CollectionDocumentation:
		if len(docs) == 0 {
			return false
		}
		pathLike := 0
		for _, d := range docs {
			if strings.Contains(d.Title, "/") {
				pathLike++
			}
		}
		return pathLike*2 > len(docs)
	default:
		return false
	}
}
