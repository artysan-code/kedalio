package mcp

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRecallDocumentsCarryOnlyTitleOriginSectionAndAShortText(t *testing.T) {
	s := newRig(t, nil).session(t)
	col := mustCall(t, s, "memory_sources", map[string]any{"op": "save", "name": "Kettle Manual"})
	mustCall(t, s, "memory_sources", map[string]any{"op": "add_document", "collection": col["id"], "file_name": "descale.md",
		"text": "# Descaling\n\n## Vinegar\n\nBoil water with vinegar to remove the flurbix from the element. " + strings.Repeat("Then rinse it twice with clean water. ", 30)})
	recall := mustCall(t, s, "memory_recall", map[string]any{"query": "flurbix"})
	docs := recall["documents"].([]any)
	if len(docs) != 1 {
		t.Fatalf("documents = %v", docs)
	}
	d := docs[0].(map[string]any)
	for k := range d {
		if k != "title" && k != "origin" && k != "section" && k != "text" {
			t.Errorf("document has %q: %v", k, d)
		}
	}
	if d["origin"] != "descale.md" || d["section"] == nil {
		t.Errorf("document = %v", d)
	}
	text := d["text"].(string)
	if n := utf8.RuneCountInString(text); n > 401 || !strings.HasSuffix(text, "…") {
		t.Errorf("text is %d characters, want at most 400 ended at a word: %q", n, text)
	}
}

func TestRecallCutsAPageBodyAt1500CharactersAndSaysWhereTheRestIs(t *testing.T) {
	r := newRig(t, nil)
	s := r.session(t)
	if _, err := r.store.Write(ctx, "notes/long.md", "# Long kettle\n\nKettle "+strings.Repeat("and the long body goes on with more words. ", 100), "owner"); err != nil {
		t.Fatal(err)
	}
	recall := mustCall(t, s, "memory_recall", map[string]any{"query": "kettle", "budget": 4000})
	pages := recall["pages"].([]any)
	if len(pages) == 0 {
		t.Fatalf("no page: %v", recall)
	}
	p := pages[0].(map[string]any)
	if n := len(p["body"].(string)); n > 1500 || p["cut"] != true {
		t.Errorf("body is %d characters, cut = %v", n, p["cut"])
	}
	if !strings.Contains(strings.Join(anyStrings(recall["notes"]), " "), "memory_read") {
		t.Errorf("notes = %v, want a hint to memory_read", recall["notes"])
	}
}

func TestMemoryReadListsOutgoingLinksAsPathsAndFlagsTheMissingOnes(t *testing.T) {
	s := newRig(t, nil).session(t)
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "notes/target.md", "body": "# Target\n\nHere.\n", "base_rev": 0})
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "notes/hub.md", "base_rev": 0,
		"body": "# Hub\n\nSee [[notes/target]] and [[notes/nowhere]].\n"})
	links := mustCall(t, s, "memory_read", map[string]any{"path": "notes/hub.md"})["links"].(map[string]any)
	out, _ := links["out"].([]any)
	if len(out) != 2 {
		t.Fatalf("out = %v", out)
	}
	var paths []string
	var missing map[string]any
	for _, o := range out {
		if p, ok := o.(string); ok {
			paths = append(paths, p)
		} else {
			missing, _ = o.(map[string]any)
		}
	}
	if len(paths) != 1 || paths[0] != "notes/target.md" {
		t.Errorf("out = %v, want the path of the page it resolves to as a string", out)
	}
	if missing["target"] != "notes/nowhere" || missing["missing"] != true || len(missing) != 2 {
		t.Errorf("out = %v, want {target, missing} for the page that is not there", out)
	}
}

func anyStrings(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, x := range l {
		out = append(out, x.(string))
	}
	return out
}
