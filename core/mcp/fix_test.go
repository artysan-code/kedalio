package mcp

import (
	"strings"
	"testing"
)

func TestARefusalNoOneWroteGivesTheConcreteFixWhereOneIsKnown(t *testing.T) {
	s := newRig(t, nil).session(t)
	out, failed := call(t, s, "memory_read", map[string]any{"path": "notes/not-there.md"})
	if !failed {
		t.Fatalf("answer %v", out)
	}
	fix := out["errors"].([]any)[0].(map[string]any)["fix"].(string)
	if strings.Contains(fix, "read the problem") || !strings.Contains(fix, "memory_recall") {
		t.Errorf("fix %q", fix)
	}
	out, failed = call(t, s, "memory_write", map[string]any{"op": "restore", "path": "notes/not-there.md", "rev": 4})
	if !failed {
		t.Fatalf("answer %v", out)
	}
	if fix := out["errors"].([]any)[0].(map[string]any)["fix"].(string); strings.Contains(fix, "read the problem") {
		t.Errorf("fix %q", fix)
	}
}
