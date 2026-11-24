package memory

import (
	"reflect"
	"testing"
)

// A hub the upkeep split into children lists them, and each child says it is part of the hub: the pages are alike
// by construction, a family and not doubles, whatever their paths.
func TestDuplicateCandidatesPagesThatLinkToEachOtherAreAFamily(t *testing.T) {
	s, _ := recallStore(t)
	write(t, s, "claude/mod-e-interfaccia.md", "---\nkind: concept\n---\n# Modes and interface\n\nText.\n\n- [[claude/status-line]]\n")
	write(t, s, "claude/status-line.md", kinded("concept", "Status line"))
	setEdge(t, s, "claude/mod-e-interfaccia.md", "claude/status-line.md", 0.98)
	write(t, s, "claude/mcp.md", kinded("concept", "MCP"))
	write(t, s, "claude/indice-permessi-mcp.md", "---\nkind: concept\n---\n# Permissions\n\nText.\n\n- part_of [[claude/mcp]]\n")
	setEdge(t, s, "claude/mcp.md", "claude/indice-permessi-mcp.md", 0.98)
	// the same pair of unrelated pages with no link stays a candidate
	write(t, s, "net/router-guide.md", kinded("concept", "Router guide"))
	write(t, s, "old/router-guide.md", kinded("concept", "Router guide"))
	setEdge(t, s, "net/router-guide.md", "old/router-guide.md", 0.95)

	got, err := s.DuplicateCandidates(ctx, AreasEN, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"net/router-guide.md|old/router-guide.md"}; !reflect.DeepEqual(keys(got), want) {
		t.Errorf("got %v", keys(got))
	}
}

// Titles alone, with no edge or one under the near edge, never make a pair: the rule needs the meaning to be near.
func TestDuplicateCandidatesNeedAnEdgeOverTheNearThreshold(t *testing.T) {
	s, _ := recallStore(t)
	write(t, s, "a/billing.md", kinded("concept", "Billing process"))
	write(t, s, "b/ledger.md", kinded("concept", "Billing process"))
	write(t, s, "c/deploy-one.md", kinded("concept", "Deploy guide"))
	write(t, s, "d/deploy-two.md", kinded("concept", "Deploy guide"))
	setEdge(t, s, "c/deploy-one.md", "d/deploy-two.md", 0)
	got, err := s.DuplicateCandidates(ctx, AreasEN, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v", got)
	}
}

// The archive keeps copies of pages that live elsewhere: a copy is never offered as a double of the live page.
func TestDuplicateCandidatesLeaveOutTheArchive(t *testing.T) {
	s, _ := recallStore(t)
	write(t, s, "progetti/archivio/personal-strix-control-card.md", kinded("entity", "Strix control"))
	write(t, s, "progetti/personal/projects/strix-control/card.md", kinded("entity", "Strix control"))
	setEdge(t, s, "progetti/archivio/personal-strix-control-card.md", "progetti/personal/projects/strix-control/card.md", 0.99)
	got, err := s.DuplicateCandidates(ctx, AreasIT, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v", got)
	}
}
