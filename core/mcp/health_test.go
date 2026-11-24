package mcp

import (
	"fmt"
	"testing"
)

func TestMemoryListHealthAndDocumentsLimit(t *testing.T) {
	s := newRig(t, nil).session(t)
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "notes/hub.md", "body": "# Hub\n\nSee [[notes/gone]].\n"})
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "notes/alone.md", "body": "# Alone\n\nNothing links here.\n"})
	h := mustCall(t, s, "memory_list", map[string]any{"what": "health"})
	if h["pages"] != float64(2) || len(h["orphans"].([]any)) != 2 {
		t.Fatalf("health %v", h)
	}
	if b := h["broken_links"].([]any); len(b) != 1 || b[0].(map[string]any)["page"] != "notes/hub.md" || b[0].(map[string]any)["link"] != "notes/gone" {
		t.Fatalf("broken %v", h["broken_links"])
	}
	if len(h["too_long"].([]any)) != 0 {
		t.Fatalf("long %v", h["too_long"])
	}

	col := mustCall(t, s, "memory_sources", map[string]any{"op": "save", "name": "Limits"})
	id := col["id"].(string)
	for _, name := range []string{"a.md", "b.md", "c.md"} {
		mustCall(t, s, "memory_sources", map[string]any{"op": "add_document", "collection": id, "file_name": name, "text": "# " + name + "\n\nText of " + name + "."})
	}
	if got := mustCall(t, s, "memory_list", map[string]any{"what": "documents", "collection": id, "limit": 2})["documents"].([]any); len(got) != 2 {
		t.Fatalf("limit 2 gave %d", len(got))
	}
}

func TestAListWithNoLimitIsShortAndSaysThereIsMore(t *testing.T) {
	s := newRig(t, nil).session(t)
	for i := range 51 {
		mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": fmt.Sprintf("notes/n%d.md", i), "body": fmt.Sprintf("# Note number %d\n\nA sentence.\n", i), "base_rev": 0, "distinct": true})
	}
	got := mustCall(t, s, "memory_list", map[string]any{"folder": "notes"})
	if docs := got["documents"].([]any); len(docs) != 20 || got["more"] == nil {
		t.Errorf("%d documents, more = %v", len(docs), got["more"])
	}
	if got := mustCall(t, s, "memory_list", map[string]any{"folder": "notes", "limit": 100}); len(got["documents"].([]any)) != 51 || got["more"] != nil {
		t.Errorf("with a limit: %v", got["more"])
	}
}

func TestADocumentListIsShortByDefaultAndSaysThereIsMore(t *testing.T) {
	s := newRig(t, nil).session(t)
	id := mustCall(t, s, "memory_sources", map[string]any{"op": "save", "name": "Many"})["id"].(string)
	for i := range 51 {
		name := fmt.Sprintf("d%d.md", i)
		mustCall(t, s, "memory_sources", map[string]any{"op": "add_document", "collection": id, "file_name": name, "text": "# " + name + "\n\nText " + name})
	}
	got := mustCall(t, s, "memory_list", map[string]any{"what": "documents", "collection": id})
	docs := got["documents"].([]any)
	if len(docs) != 50 || got["more"] == nil {
		t.Errorf("%d documents, more = %v", len(docs), got["more"])
	}
	for _, k := range []string{"collection", "hash", "added", "version"} {
		if _, has := docs[0].(map[string]any)[k]; has {
			t.Errorf("a document row carries %q", k)
		}
	}
}

func TestAPageListRowsHaveNoCreatedOrBy(t *testing.T) {
	s := newRig(t, nil).session(t)
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "notes/a.md", "body": "# A\n\nText.\n", "base_rev": 0})
	row := mustCall(t, s, "memory_list", map[string]any{"folder": "notes"})["documents"].([]any)[0].(map[string]any)
	for _, k := range []string{"created", "by"} {
		if _, has := row[k]; has {
			t.Errorf("row carries %q", k)
		}
	}
	if row["path"] == nil || row["title"] == nil || row["updated"] == nil {
		t.Errorf("row %v", row)
	}
}

func TestAListLimitAboveAThousandIsRefusedSoTheMoreNoteIsTrue(t *testing.T) {
	s := newRig(t, nil).session(t)
	for i := range 1001 {
		mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": fmt.Sprintf("notes/n%d.md", i), "body": fmt.Sprintf("# Note number %d\n\nA sentence.\n", i), "base_rev": 0, "distinct": true})
	}
	if _, failed := call(t, s, "memory_list", map[string]any{"folder": "notes", "limit": 5000}); !failed {
		t.Error("a limit over 1000 is refused, not honoured")
	}
	got := mustCall(t, s, "memory_list", map[string]any{"folder": "notes", "limit": 1000})
	if docs := got["documents"].([]any); len(docs) != 1000 || got["more"] == nil {
		t.Errorf("%d documents, more = %v", len(docs), got["more"])
	}
}
