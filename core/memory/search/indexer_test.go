package search_test

import (
	"kedalio/core/memory"
	"kedalio/core/memory/search"
)

// The page store reaches the index only through memory.Indexer.
var _ memory.Indexer = (*search.Index)(nil)
