package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"kedalio/core/memory"
)

// seedRevisions writes a task page three times into a data folder a core made: a credential-looking value in @1 and
// @2, gone from @3 (the live one). The value is made at run time, so no scanner of the repository takes it for a leak.
func seedRevisions(t *testing.T, dir string) (value string) {
	t.Helper()
	value = "n8n" + strings.Repeat("Xw4", 9)
	d, err := openData(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := memory.New(d, nil)
	for _, b := range []string{
		"# Sync\n\n## Steps\n- [ ] call it with N8N_HTTP_TOKEN=" + value + "\n",
		"# Sync\n\n## Steps\n- [ ] call it with N8N_HTTP_TOKEN=" + value + "\n- [ ] check the log\n",
		"# Sync\n\n## Steps\n- [ ] call it with N8N_HTTP_TOKEN (in sops)\n- [ ] check the log\n",
	} {
		if _, err := s.Write(context.Background(), "tasks/t-20261002-abc123.md", b, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	return value
}

func revisionBody(t *testing.T, dir string, rev int) string {
	t.Helper()
	d, err := openData(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	r, err := memory.New(d, nil).Revision(context.Background(), "tasks/t-20261002-abc123.md", rev)
	if err != nil || r == nil {
		t.Fatalf("@%d: %v", rev, err)
	}
	return r.Body
}

func TestRevisionsRedactTakesTheValueOutOfTheNamedRevisionsOnly(t *testing.T) {
	dir := t.TempDir()
	started(t, testOptions(dir))
	value := seedRevisions(t, dir)
	ctx := context.Background()
	live := revisionBody(t, dir, 3)

	var out bytes.Buffer
	if err := runRevisions(ctx, []string{"redact", "-data", dir, "-dry-run", "tasks/t-20261002-abc123.md@1", "tasks/t-20261002-abc123.md@3"}, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "@1: would replace 1\n") || !strings.Contains(got, "@3: would replace 0 (live page)") {
		t.Errorf("dry run said:\n%s", got)
	}
	if !strings.Contains(revisionBody(t, dir, 1), value) {
		t.Fatal("the dry run changed @1")
	}

	out.Reset()
	if err := runRevisions(ctx, []string{"redact", "-data", dir, "tasks/t-20261002-abc123.md@1"}, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "tasks/t-20261002-abc123.md@1: replaced 1\n") || strings.Contains(got, value) {
		t.Errorf("the run said:\n%s", got)
	}
	r1 := revisionBody(t, dir, 1)
	if strings.Contains(r1, value) || !strings.HasSuffix(r1, "## Steps\n- [ ] call it with N8N_HTTP_TOKEN=‹hidden — look it up in the service's own panel›\n") {
		t.Errorf("@1 is %q", r1)
	}
	if !strings.Contains(revisionBody(t, dir, 2), value) {
		t.Error("@2 was touched though not named")
	}
	if revisionBody(t, dir, 3) != live {
		t.Error("the live page changed")
	}
}

func TestRevisionsRefusesBadUsageBeforeTouchingAnything(t *testing.T) {
	var out bytes.Buffer
	for _, args := range [][]string{{}, {"nope"}, {"redact"}, {"redact", "-data", t.TempDir()}} {
		if err := runRevisions(context.Background(), args, &out); err == nil || !strings.Contains(err.Error(), "usage") {
			t.Errorf("%v: %v", args, err)
		}
	}
	for _, args := range [][]string{{"redact", "-data", t.TempDir(), "a.md@x"}, {"redact", "-data", t.TempDir(), "a.md@0"}, {"redact", "-data", t.TempDir(), "a.md"}} {
		if err := runRevisions(context.Background(), args, &out); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
	if out.Len() != 0 {
		t.Errorf("refusals printed %q", out.String())
	}
}
