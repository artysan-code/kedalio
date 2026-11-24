package search

import (
	"context"
	"testing"
)

func TestQueryCacheEvictsTheLeastRecentlyUsedAndAGetRefreshesAnEntry(t *testing.T) {
	c := newQueryCache(2)
	c.put("a", []float32{1})
	c.put("b", []float32{2})
	if _, ok := c.get("a"); !ok { // a is now the most recent, b the oldest
		t.Fatal("a missing")
	}
	c.put("c", []float32{3})
	if _, ok := c.get("b"); ok {
		t.Error("b should have been evicted")
	}
	for k, want := range map[string]float32{"a": 1, "c": 3} {
		if v, ok := c.get(k); !ok || v[0] != want {
			t.Errorf("get(%q) = %v, %v", k, v, ok)
		}
	}
	if len(c.items) != 2 || c.order.Len() != 2 {
		t.Errorf("size: %d items, %d in order", len(c.items), c.order.Len())
	}
	if _, ok := c.get("zzz"); ok {
		t.Error("a key never put")
	}
}

func TestUpdateCollectionEmptiesTheHubWithAnEmptyStringAndLeavesItWithNil(t *testing.T) {
	ix := newTestIndex(t)
	ctx := context.Background()
	hub, empty := "notes/hub.md", ""
	id, err := ix.CreateCollection(ctx, "Docs", CollectionFields{Hub: &hub})
	if err != nil {
		t.Fatal(err)
	}
	read := func() string {
		c, err := ix.Collection(ctx, id)
		if err != nil || c == nil {
			t.Fatal(c, err)
		}
		return c.Hub
	}
	if read() != hub {
		t.Fatalf("hub = %q", read())
	}
	imp := true
	if err := ix.UpdateCollection(ctx, id, CollectionFields{Important: &imp}); err != nil || read() != hub {
		t.Errorf("a field left out keeps the hub: %q, %v", read(), err)
	}
	if err := ix.UpdateCollection(ctx, id, CollectionFields{Hub: &empty}); err != nil || read() != "" {
		t.Errorf("hub = %q, %v", read(), err)
	}
}
