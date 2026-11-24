package offsite

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A genuine tag copied under a far-future name carries the old signed time: it must not count as a newer copy.
func TestAGenuineTagUnderAFutureNameDoesNotBlockTheRestore(t *testing.T) {
	c := signedStore(t, nil)
	var old []byte
	// the older copy's tag is read back through the store, then replayed under a name from the future
	buf := &limitedBuffer{}
	if _, err := c.Get(context.Background(), olderCopy+".tag", buf); err != nil {
		t.Fatal(err)
	}
	old = buf.Bytes()
	src := filepath.Join(t.TempDir(), "replayed.tag")
	if err := os.WriteFile(src, old, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.Put(context.Background(), "core-20990101T000000Z.db.tag", src); err != nil {
		t.Fatal(err)
	}
	if name, err := Restore(context.Background(), c, "", t.TempDir(), RestoreOptions{Key: tagKey}); err != nil || name != newestCopy {
		t.Fatalf("%s %v", name, err)
	}
}
