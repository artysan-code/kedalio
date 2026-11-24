package memory

import (
	"fmt"
	"strings"
	"testing"

	"kedalio/core/memory/search"
)

func TestRecallLeavesOutADocumentThatHoldsOnlyTheGenericWordsOfAQueryThatNamesAPage(t *testing.T) {
	s, ix := recallStore(t)
	col, err := ix.CreateCollection(ctx, "Altro progetto", search.CollectionFields{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ix.AddDocument(ctx, search.NewDocument{Collection: col, Kind: search.KindMarkdown, Title: "STATO", Origin: "STATO.md",
		Sections: []search.Section{{Text: "# Stato\n\nLo stato del lavoro e i prossimi passi: rifare il cruscotto, poi la migrazione."}}}); err != nil {
		t.Fatal(err)
	}
	write(t, s, "projects/twin-falcon.md", pageOf("Twin di Falcon", "Il twin di Falcon gira sul server di casa."))
	a := recall(t, s, ix, "Twin di Falcon: stato e prossimi passi", RecallOptions{Budget: 4000})
	if len(a.Pages) == 0 || a.Pages[0].Path != "projects/twin-falcon.md" {
		t.Fatalf("pages = %v", shown(a))
	}
	for _, d := range a.Documents {
		t.Errorf("document %q is another project's: it holds 3 of the 5 words, the generic ones", d.Title)
	}
}

func TestRecallListsNoMoreRelatedPagesThanSix(t *testing.T) {
	s, ix := recallStore(t)
	var hub strings.Builder
	hub.WriteString("# Espresso hub\n\nThe espresso hub.\n\n")
	for i := 0; i < 15; i++ {
		fmt.Fprintf(&hub, "- [[notes/out-%02d]]\n", i)
		write(t, s, fmt.Sprintf("notes/out-%02d.md", i), pageOf(fmt.Sprintf("Out %02d", i), "Short page."))
	}
	write(t, s, "notes/espresso-hub.md", hub.String())
	a := recall(t, s, ix, "espresso hub", RecallOptions{Budget: 4000})
	if len(a.Related) > 6 || DefaultRecallWeights.RelatedMax != 6 {
		t.Errorf("related = %d, RelatedMax = %d, want at most 6", len(a.Related), DefaultRecallWeights.RelatedMax)
	}
}
