package mcp

import (
	"strings"
	"testing"
)

func TestHubSaysTheToolByItsNameAtRenderWhateverTheOldTextSays(t *testing.T) {
	s := newRig(t, nil).session(t)
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "sources/old-hub.md",
		"body": "# Old hub\n\nCollection of documents. Search it with memory.search, or read it with memory.read.\n"})
	r := mustCall(t, s, "memory_read", map[string]any{"path": "sources/old-hub.md"})
	if b := r["body"].(string); !strings.Contains(b, "Search it with memory_recall, or read it with memory_read.") || strings.Contains(b, "memory.") {
		t.Fatalf("body %q", b)
	}
}
