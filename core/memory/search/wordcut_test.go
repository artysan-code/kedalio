package search

import (
	"strings"
	"testing"
)

// A paragraph with no blank line in it, longer than a piece (an extracted PDF page): cut through a word, the next
// piece's overlap carried the first half of the word in front of the second («P\n\nRODUZIONE» in a citation).
func TestChunkALongParagraphIsNeverCutThroughAWord(t *testing.T) {
	words := strings.Fields(strings.Repeat("INTRODUZIONE produzione. ", 40))
	got := chunk("# H\n\n"+strings.Join(words, " "), 90, "")
	if len(got) < 3 {
		t.Fatalf("pieces: %d", len(got))
	}
	for _, p := range got {
		for _, w := range strings.Fields(strings.ReplaceAll(strings.TrimPrefix(p.text, "# H"), "\n", " ")) {
			if w != "INTRODUZIONE" && w != "produzione." {
				t.Fatalf("a torn word %q in %q", w, p.text)
			}
		}
	}
}

func TestChunkARunWithNoSpaceIsStillCutAtSize(t *testing.T) {
	got := chunk(strings.Repeat("x", 25), 12, "")
	if len(got) != 3 || got[0].text != strings.Repeat("x", 12) {
		t.Fatalf("got %q", texts(got))
	}
}
