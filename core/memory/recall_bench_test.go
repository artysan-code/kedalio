package memory

import (
	"fmt"
	"path/filepath"
	"testing"

	"kedalio/core/db"
	"kedalio/core/memory/search"
)

// benchRecall times one recall over a file database of n pages that link to their neighbours.
func benchRecall(b *testing.B, n int) {
	sqldb, err := db.Open(filepath.Join(b.TempDir(), "k.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { sqldb.Close() })
	if err := Migrate(ctx, sqldb); err != nil {
		b.Fatal(err)
	}
	ix, err := search.New(ctx, sqldb)
	if err != nil {
		b.Fatal(err)
	}
	s := italian(New(sqldb, ix))
	for i := range n {
		body := pageOf(fmt.Sprintf("Subject %d", i), fmt.Sprintf(
			"The pump number %d runs at %d bar, see [[note/p%d]] and [[note/p%d]].\n", i, i%7, (i+1)%n, (i*7+3)%n))
		if _, err := s.Write(ctx, fmt.Sprintf("note/p%d.md", i), body, "bench"); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Recall(ctx, "pump number 5", RecallOptions{Index: ix}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRecall100(b *testing.B)  { benchRecall(b, 100) }
func BenchmarkRecall1000(b *testing.B) { benchRecall(b, 1000) }
