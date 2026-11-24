package mcp

import "testing"

func TestSimilarityAOneWordTitleIsNotTheDuplicateOfALongTitleThatHasTheWord(t *testing.T) {
	if s := similarity("Rete", "Come configurare la rete domestica con il router"); s >= duplicate {
		t.Errorf("one shared word of a long title: %v", s)
	}
	if similarity("Router", "router") != 1 {
		t.Error("the same single word is the same title")
	}
	if similarity("Project Alpha", "Alpha project notes") != 1 {
		t.Error("two words contained in a longer title stay a duplicate")
	}
}
