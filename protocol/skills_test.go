package protocol

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// toolLike matches what a skill writes when it names a Kedalio tool; the prefixes are the tool families of tools.go.
var toolLike = regexp.MustCompile(`\b(?:memory|tasks|session|projects|settings|clients)_[a-z_]+\b`)

var toolNames = map[string]bool{
	ToolMemoryRecall: true, ToolMemoryRead: true, ToolMemoryList: true, ToolMemoryWrite: true,
	ToolMemoryDelete: true, ToolMemorySources: true, ToolMemoryUpkeep: true,
	ToolTasksBrief: true, ToolTasksList: true, ToolTasksGet: true, ToolTasksSave: true,
	ToolSessionStart: true, ToolProjectsLink: true, ToolSettingsGet: true, ToolSettingsSet: true, ToolClientsApprove: true,
}

// unknownTools lists the tool-like names in text that are not tools.
func unknownTools(text string) []string {
	var bad []string
	for _, n := range toolLike.FindAllString(text, -1) {
		if !toolNames[n] {
			bad = append(bad, n)
		}
	}
	return bad
}

func TestUnknownToolsFlagsAnInventedName(t *testing.T) {
	got := unknownTools("call `memory_frobnicate` then `tasks_save`")
	if len(got) != 1 || got[0] != "memory_frobnicate" {
		t.Fatalf("got %v", got)
	}
}

// The plugin's skills call tools by name; a rename in tools.go must reach them in the same commit.
func TestPluginSkillsNameOnlyRealTools(t *testing.T) {
	root := filepath.Join("..", "plugin", "skills")
	n := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		n++
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, name := range unknownTools(string(b)) {
			t.Errorf("%s names %q, which is not a tool in tools.go", p, name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no skill found under plugin/skills")
	}
}
