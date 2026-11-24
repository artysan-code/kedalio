package db

import (
	"testing"
	"time"
)

func TestCopyTimeReadsTheStampOfACopyName(t *testing.T) {
	got, ok := CopyTime("/backups/core-20260105T093000Z.db")
	if want := time.Date(2026, 1, 5, 9, 30, 0, 0, time.UTC); !ok || !got.Equal(want) {
		t.Errorf("got %v, %v, want %v", got, ok, want)
	}
	for _, name := range []string{"core.db", "core-20260105T093000Z.db.tmp", "other-20260105T093000Z.db", "core-yesterday.db", ""} {
		if _, ok := CopyTime(name); ok {
			t.Errorf("%q is not a copy name", name)
		}
	}
}
