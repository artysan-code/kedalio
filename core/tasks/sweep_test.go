package tasks

import (
	"strings"
	"testing"
	"time"
)

// oldTask is a task page as the old shape wrote it, with the project as a bare slug.
func oldTask(id, title string) string {
	return "---\nid: \"" + id + "\"\nkind: task\ntitle: \"" + title + "\"\nstatus: \"todo\"\nproject: \"kedalio\"\n" +
		"created: \"2026-10-01T09:00:00.000Z\"\nupdated: \"2026-10-02T09:00:00.000Z\"\n---\n\n# " + title + "\n\nNotes.\n"
}

func TestReshapeRewritesOldShapeKeepingUpdated(t *testing.T) {
	b := newBook(t)
	write(t, b, "progetti/kedalio.md", "# Kedalio\n\nIl server personale.\n")
	write(t, b, "tasks/t-20261001-aaaa.md", oldTask("t-20261001-aaaa", "Old one"))
	res, err := b.Reshape(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, res.Rewritten, 1)
	eq(t, res.Left, 0)
	eq(t, len(res.Kept), 0)
	doc, _ := b.Store.Get(ctx, "tasks/t-20261001-aaaa.md")
	if strings.Contains(doc.Body, "\nproject:") || !strings.Contains(doc.Body, "- part_of [[progetti/kedalio]]") {
		t.Errorf("not in the new shape:\n%s", doc.Body)
	}
	got := mustGet(t, b, "t-20261001-aaaa")
	eq(t, got.Updated, "2026-10-02T09:00:00.000Z")
	eq(t, got.Project, "kedalio")

	again, err := b.Reshape(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, again.Rewritten, 0)
	eq(t, again.Left, 0)
}

func TestReshapeHonoursLimitAndCountsLeft(t *testing.T) {
	b := newBook(t)
	for _, id := range []string{"t-20261001-aaaa", "t-20261001-bbbb", "t-20261001-cccc"} {
		write(t, b, "tasks/"+id+".md", oldTask(id, id))
	}
	res, err := b.Reshape(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, res.Rewritten, 2)
	eq(t, res.Left, 1)
	res, err = b.Reshape(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, res.Rewritten, 1)
	eq(t, res.Left, 0)
}

func TestReshapeMovesLogAndTidiesNotes(t *testing.T) {
	b := newBook(t)
	task := mustAdd(t, b, Input{Title: "Has a log"})
	doc, _ := b.Store.Get(ctx, "tasks/"+task.ID+".md")
	body := strings.Replace(doc.Body, "# Has a log\n", "# Has a log\n\n## Log\n\n- 2026-10-20 · did it\n", 1)
	write(t, b, "tasks/"+task.ID+".md", body)
	res, err := b.Reshape(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, res.Rewritten, 1)
	log, _ := b.Store.Get(ctx, LogPath(task.ID))
	if log == nil || !strings.Contains(log.Body, "2026-10-20 · did it") {
		t.Errorf("log not moved: %+v", log)
	}
	eq(t, mustGet(t, b, task.ID).Updated, task.Updated)
}

func TestReshapeKeepsATaskThatWouldNotReadBackTheSame(t *testing.T) {
	b := newBook(t)
	// an empty extra field is dropped when the task is written, so the file would not read back the same
	body := "---\nid: \"t-20261001-kkkk\"\nkind: task\ntitle: \"Odd\"\nstatus: \"todo\"\nproject: \"x\"\nnote: \"\"\n---\n\n# Odd\n"
	write(t, b, "tasks/t-20261001-kkkk.md", body)
	res, err := b.Reshape(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, res.Rewritten, 0)
	if len(res.Kept) != 1 || res.Kept[0] != "t-20261001-kkkk" {
		t.Fatalf("kept %v", res.Kept)
	}
	doc, _ := b.Store.Get(ctx, "tasks/t-20261001-kkkk.md")
	eq(t, doc.Body, body)
}

func TestLongLogs(t *testing.T) {
	b := newBook(t)
	mk := func(id string, words int, summary string) {
		entry := []string{"2026-10-01 · " + strings.TrimSpace(strings.Repeat("word ", words-2))}
		body, err := logBody(id, "T", entry, summary)
		if err != nil {
			t.Fatal(err)
		}
		write(t, b, LogPath(id), body)
	}
	mk("t-20261001-aaaa", 301, "")
	mk("t-20261001-bbbb", 300, "")
	mk("t-20261001-cccc", 400, "## Summary\n\nShort.")
	got, err := b.LongLogs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "t-20261001-aaaa" || got[0].Words != 301 {
		t.Fatalf("got %+v", got)
	}
	mk("t-20261001-dddd", 500, "")
	got, _ = b.LongLogs(ctx, 1)
	eq(t, len(got), 1)
}

func TestReshapeWaitsForTheBookAndReadsEachTaskFresh(t *testing.T) {
	b := newBook(t)
	write(t, b, "tasks/t-20261001-aaaa.md", oldTask("t-20261001-aaaa", "Old one"))
	b.mu.Lock() // a change to the book is under way
	done := make(chan error, 1)
	go func() { _, err := b.Reshape(ctx, 50); done <- err }()
	select {
	case <-done:
		t.Fatal("the sweep ran while the book was being changed")
	case <-time.After(100 * time.Millisecond):
	}
	// the change lands before the sweep looks at the page: the sweep must see it, not an older copy
	write(t, b, "tasks/t-20261001-aaaa.md", strings.Replace(oldTask("t-20261001-aaaa", "Old one"), "Notes.", "Notes changed meanwhile.", 1))
	b.mu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	doc, _ := b.Store.Get(ctx, "tasks/t-20261001-aaaa.md")
	if !strings.Contains(doc.Body, "Notes changed meanwhile.") {
		t.Errorf("the sweep wrote an older copy:\n%s", doc.Body)
	}
}
