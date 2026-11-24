package mcp

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kedalio/core/memory"
	"kedalio/core/memory/docs"
)

func TestSourcesSaveAddListSearchRecallAndDelete(t *testing.T) {
	s := newRig(t, nil).session(t)
	col := mustCall(t, s, "memory_sources", map[string]any{"op": "save", "name": "Kettle Manual", "version": "2.0",
		"description": "How the kettle works."})
	id := col["id"].(string)
	if col["hub"] != "sources/kettle-manual.md" || col["version"] != "2.0" || col["documents"] != float64(0) || col["important"] != false {
		t.Fatalf("collection %v", col)
	}
	hub := mustCall(t, s, "memory_read", map[string]any{"path": "sources/kettle-manual.md"})
	if hub["kind"] != "source" || !strings.Contains(hub["body"].(string), "Collection of documents, version 2.0") {
		t.Fatalf("hub %v", hub)
	}

	added := mustCall(t, s, "memory_sources", map[string]any{"op": "add_document", "collection": id, "file_name": "descale.md",
		"text": "# Descaling\n\n## Vinegar\n\nBoil water with vinegar to remove the flurbix from the element."})
	if added["kind"] != "markdown" || added["change"] != "added" || added["chunks"] != float64(2) || added["original"] != nil {
		t.Fatalf("added %v", added)
	}
	pdf := mustCall(t, s, "memory_sources", map[string]any{"op": "add_document", "collection": id, "file_name": "safety.txt",
		"base64": base64.StdEncoding.EncodeToString([]byte("Never open the lid while the kettle boils."))})
	if o := pdf["original"].(map[string]any); o["name"] != "safety.txt" || o["type"] != "text/plain" || o["size"] != float64(42) {
		t.Fatalf("added %v", pdf)
	}

	cols := mustCall(t, s, "memory_list", map[string]any{"what": "collections"})["collections"].([]any)
	if len(cols) != 1 || cols[0].(map[string]any)["documents"] != float64(2) {
		t.Fatalf("collections %v", cols)
	}
	docsOut := mustCall(t, s, "memory_list", map[string]any{"what": "documents", "collection": id})["documents"].([]any)
	if len(docsOut) != 2 || docsOut[0].(map[string]any)["title"] != "descale.md" || docsOut[0].(map[string]any)["superseded"] != nil {
		t.Fatalf("documents %v", docsOut)
	}
	// the hub lists them
	hub = mustCall(t, s, "memory_read", map[string]any{"path": "sources/kettle-manual.md"})
	if !strings.Contains(hub["body"].(string), "- descale.md (markdown)") {
		t.Fatalf("hub %v", hub["body"])
	}

	found := mustCall(t, s, "memory_recall", map[string]any{"mode": "search", "query": "flurbix"})["results"].([]any)
	r := found[0].(map[string]any)
	c := r["citations"].([]any)[0].(map[string]any)
	if r["collection"] != "Kettle Manual" || r["document"] != added["id"] || r["version"] != "2.0" || r["origin"] != "descale.md" ||
		r["path"] != nil || c["section"] != "descale.md › Descaling › Vinegar" || c["at"].(map[string]any)["heading"] != "Vinegar" {
		t.Fatalf("search %v", r)
	}
	// a version nobody wrote under finds nothing
	if got := mustCall(t, s, "memory_recall", map[string]any{"mode": "search", "query": "flurbix", "version": "9"})["results"].([]any); len(got) != 0 {
		t.Fatalf("version 9: %v", got)
	}

	recall := mustCall(t, s, "memory_recall", map[string]any{"query": "flurbix"})
	d := recall["documents"].([]any)
	if len(d) != 1 || d[0].(map[string]any)["origin"] != "descale.md" || d[0].(map[string]any)["section"] == nil {
		t.Fatalf("recall %v", recall)
	}

	if rm := mustCall(t, s, "memory_delete", map[string]any{"what": "document", "collection": id, "id": pdf["id"]}); rm["removed"] != "safety.txt" {
		t.Fatalf("removed %v", rm)
	}
	del := mustCall(t, s, "memory_delete", map[string]any{"what": "collection", "id": id})["deleted"].(map[string]any)
	if del["name"] != "Kettle Manual" || del["documents"] != float64(1) {
		t.Fatalf("deleted %v", del)
	}
	if _, failed := call(t, s, "memory_read", map[string]any{"path": "sources/kettle-manual.md"}); !failed {
		t.Error("the hub is still there")
	}
}

func TestSourcesRefusalsAreAnswersNotErrors(t *testing.T) {
	s := newRig(t, nil).session(t)
	col := mustCall(t, s, "memory_sources", map[string]any{"op": "save", "name": "Docs"})
	id := col["id"].(string)
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"op": "save", "id": "nope", "name": "x"}, "no collection nope"},
		{map[string]any{"op": "save", "name": "Docs"}, "exists already"},
		{map[string]any{"op": "add_document", "collection": "nope", "file_name": "a.md", "text": "x"}, "no collection nope"},
		{map[string]any{"op": "add_document", "collection": id, "text": "x"}, "has a name"},
		{map[string]any{"op": "add_document", "collection": id, "file_name": "a.md", "text": "x", "base64": "eA=="}, "not both"},
		{map[string]any{"op": "add_document", "collection": id, "file_name": "a.md", "base64": "!!!"}, "not valid base64"},
		{map[string]any{"op": "add_document", "collection": id}, "a url or as a file"},
		{map[string]any{"op": "add_document", "collection": id, "file_name": "a.exe", "text": "x"}, "type not supported"},
		{map[string]any{"op": "add_document", "collection": id, "url": "http://127.0.0.1:1/a.md"}, "private address"},
		{map[string]any{"op": "add_document", "collection": id, "url": "https://user:pw@example.com/a.md"}, "credentials"},
	} {
		out, failed := call(t, s, "memory_sources", c.args)
		errs, _ := out["errors"].([]any)
		if failed || out["refused"] != true || len(errs) != 1 || !strings.Contains(errs[0].(string), c.want) {
			t.Errorf("%v: failed %v, got %v, want %q", c.args, failed, out, c.want)
		}
	}
	// what the flat schema leaves optional is asked for in the connector's words
	for _, args := range []map[string]any{{"op": "save"}, {"op": "add_document"}} {
		if out, failed := call(t, s, "memory_sources", args); !failed || out["errors"] == nil {
			t.Errorf("%v: got %v", args, out)
		}
	}
	if out, failed := call(t, s, "memory_list", map[string]any{"what": "documents"}); !failed {
		t.Errorf("documents of no collection: %v", out)
	}
}

func TestSourcesAddsAURLAndReplacesItWhenItChanges(t *testing.T) {
	text := "# Page\n\nfirst version of the sprocket guide"
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/data.bin" {
			w.Header().Set("Content-Type", "application/octet-stream")
		} else {
			w.Header().Set("Content-Type", "text/markdown")
		}
		w.Write([]byte(text))
	}))
	defer web.Close()
	rg := newRig(t, nil)
	// the test serves from the loopback, which the guarded fetcher refuses; the guard has its own tests in docs
	rg.srv.collections.Fetch = &docs.Fetcher{Client: web.Client()}
	s := rg.session(t)
	id := mustCall(t, s, "memory_sources", map[string]any{"op": "save", "name": "Web"})["id"].(string)
	a := mustCall(t, s, "memory_sources", map[string]any{"op": "add_document", "collection": id, "url": web.URL + "/guide.md"})
	if a["origin"] != web.URL+"/guide.md" || a["title"] != "guide.md" || a["change"] != "added" || a["original"] == nil {
		t.Fatalf("added %v", a)
	}
	text = "# Page\n\nsecond version of the sprocket guide"
	b := mustCall(t, s, "memory_sources", map[string]any{"op": "add_document", "collection": id, "url": web.URL + "/guide.md"})
	if b["change"] != "updated" || b["id"] != a["id"] {
		t.Fatalf("again %v", b)
	}
	got := mustCall(t, s, "memory_recall", map[string]any{"mode": "search", "query": "sprocket"})["results"].([]any)
	if len(got) != 1 || !strings.Contains(got[0].(map[string]any)["citations"].([]any)[0].(map[string]any)["text"].(string), "second") {
		t.Fatalf("search %v", got)
	}
	out, _ := call(t, s, "memory_sources", map[string]any{"op": "add_document", "collection": id, "url": web.URL + "/data.bin"})
	if out["refused"] != true || !strings.Contains(out["errors"].([]any)[0].(string), "type not supported") {
		t.Fatalf("got %v", out)
	}
}

func TestRefusedOrFailedAnswersOnlyTheCallersMistakesAsRefusals(t *testing.T) {
	if out, err := refusedOrFailed(&memory.RefusedError{Msg: "no collection x"}); err != nil || out.(refused).Refused != true {
		t.Errorf("a refusal = %v %v", out, err)
	}
	if out, err := refusedOrFailed(errors.New("database is locked")); err == nil || out != nil {
		t.Errorf("a store failure = %v %v", out, err)
	}
}
