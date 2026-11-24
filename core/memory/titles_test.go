package memory

import "testing"

func TestTitleSimilarityContainmentJaccardAndFolding(t *testing.T) {
	if TitleSimilarity("Project Alpha", "alpha project notes") != 1 || TitleSimilarity("Cats", "Dogs") != 0 {
		t.Error("containment is 1, no shared word 0")
	}
	if got := TitleSimilarity("Rete", "Come configurare la rete domestica con il router"); got >= 0.6 {
		t.Errorf("a one-word title is judged by Jaccard alone: %v", got)
	}
	if TitleSimilarity("Città", "citta") != 1 || TitleSimilarity("", "x") != 0 {
		t.Error("accents fold, an empty title is like nothing")
	}
	// "go" and "to" are two letters: left out
	if w := TitleWords("Go to Café"); len(w) != 1 || !w["cafe"] {
		t.Errorf("words = %v", w)
	}
}
