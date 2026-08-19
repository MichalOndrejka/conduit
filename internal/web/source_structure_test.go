package web

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
)

func point(id, docID, title string, chunkIndex, total int, extra map[string]any) rag.ScrolledPoint {
	payload := map[string]any{
		models.PayloadSourceDocID: docID,
		models.PayloadChunkIndex:  strconv.Itoa(chunkIndex),
		models.PayloadTotalChunks: strconv.Itoa(total),
		models.PropKey("title"):   title,
	}
	for k, v := range extra {
		payload[k] = v
	}
	raw, _ := json.Marshal(id)
	return rag.ScrolledPoint{ID: raw, Payload: payload}
}

func TestBuildDocSummariesGroupsByDoc(t *testing.T) {
	points := []rag.ScrolledPoint{
		point("p1", "doc-a", "src/foo.go", 0, 2, nil),
		point("p2", "doc-a", "src/foo.go", 1, 2, nil),
		point("p3", "doc-b", "src/bar.go", 0, 1, nil),
	}
	docs := buildDocSummaries(points)
	if len(docs) != 2 {
		t.Fatalf("got %d docs, want 2", len(docs))
	}
	byTitle := map[string]docSummary{}
	for _, d := range docs {
		byTitle[d.Title] = d
	}
	if byTitle["src/foo.go"].ChunkCount != 2 {
		t.Errorf("src/foo.go ChunkCount = %d, want 2", byTitle["src/foo.go"].ChunkCount)
	}
	if byTitle["src/bar.go"].ChunkCount != 1 {
		t.Errorf("src/bar.go ChunkCount = %d, want 1", byTitle["src/bar.go"].ChunkCount)
	}
}

func TestBuildDocSummariesFallsBackToDocID(t *testing.T) {
	points := []rag.ScrolledPoint{
		point("p1", "doc-a", "", 0, 1, nil),
	}
	docs := buildDocSummaries(points)
	if len(docs) != 1 || docs[0].Title != "doc-a" {
		t.Fatalf("got %+v, want title fallback to doc-a", docs)
	}
}

func TestBuildTreeGroupsDirectories(t *testing.T) {
	docs := []docSummary{
		{DocID: "1", Title: "src/auth/login.go", ChunkCount: 1},
		{DocID: "2", Title: "src/auth/session.go", ChunkCount: 2},
		{DocID: "3", Title: "src/web/routes.go", ChunkCount: 3},
		{DocID: "4", Title: "README.md", ChunkCount: 1},
	}
	root := buildTree(docs)

	// Top level: directory "src" before file "README.md".
	if len(root.Children) != 2 {
		t.Fatalf("got %d top-level children, want 2", len(root.Children))
	}
	if root.Children[0].Name != "src" || root.Children[0].IsLeaf {
		t.Errorf("first child = %+v, want directory src", root.Children[0])
	}
	if root.Children[1].Name != "README.md" || !root.Children[1].IsLeaf {
		t.Errorf("second child = %+v, want leaf README.md", root.Children[1])
	}

	src := root.Children[0]
	if len(src.Children) != 2 {
		t.Fatalf("src has %d children, want 2 (auth, web)", len(src.Children))
	}
	auth := src.Children[0]
	if auth.Name != "auth" || len(auth.Children) != 2 {
		t.Fatalf("auth = %+v, want 2 leaf children", auth)
	}
	if auth.Children[0].Name != "login.go" || auth.Children[0].Doc.ChunkCount != 1 {
		t.Errorf("login.go leaf = %+v", auth.Children[0])
	}
}

func TestUseTree(t *testing.T) {
	pathDocs := []docSummary{{Title: "src/foo.go"}, {Title: "src/bar.go"}}
	flatDocs := []docSummary{{Title: "Some Work Item"}, {Title: "Another Item"}}

	if !useTree(models.CollectionCode, flatDocs) {
		t.Error("code collection should always use tree, even without path-shaped titles")
	}
	if !useTree(models.CollectionTestCode, flatDocs) {
		t.Error("test-code collection should always use tree")
	}
	if useTree(models.CollectionWorkItems, pathDocs) {
		t.Error("work-item collection should never use tree")
	}
	if !useTree(models.CollectionDocumentation, pathDocs) {
		t.Error("documentation collection with majority path-shaped titles should use tree")
	}
	if useTree(models.CollectionDocumentation, flatDocs) {
		t.Error("documentation collection without path-shaped titles should stay flat")
	}
	if useTree(models.CollectionDocumentation, nil) {
		t.Error("documentation collection with no docs should stay flat")
	}
}
