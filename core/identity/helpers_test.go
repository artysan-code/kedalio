package identity

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"kedalio/core/db"
)

var ctx = context.Background()

const testURL = "https://core.example.com"

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func testIdentity(t *testing.T) *Identity {
	t.Helper()
	i, err := New(ctx, testDB(t), Options{PublicURL: testURL})
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func jsonBody(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}
