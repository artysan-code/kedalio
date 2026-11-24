package vault

import (
	"bytes"
	"testing"
)

// The command makes the key file before the database exists (the database is encrypted with a key derived from it), and
// Open then adopts it: the instance is unlocked and its recovery code is still shown once.
func TestOpenAdoptsAKeyFileMadeBeforeTheDatabase(t *testing.T) {
	dir := t.TempDir()
	if raw, err := HostKey(dir, false); raw != nil || err != nil {
		t.Fatalf("no file, no create: %v %v", raw, err)
	}
	raw, err := HostKey(dir, true)
	must(t, err)
	again, err := HostKey(dir, false)
	must(t, err)
	if !bytes.Equal(raw, again) {
		t.Fatal("the key file is read back as made")
	}
	v, err := Open(ctx, newDB(t), dir)
	must(t, err)
	if v.Locked() {
		t.Fatal("adopted key: unlocked")
	}
	code, err := v.TakeRecoveryCode(ctx)
	must(t, err)
	if code != RecoveryCode(raw) {
		t.Fatalf("code %q", code)
	}
	if again, _ := v.TakeRecoveryCode(ctx); again != "" {
		t.Fatal("shown twice")
	}
}

func TestDatabaseKeyIsDerivedAndNotTheInstanceKey(t *testing.T) {
	kek := bytes.Repeat([]byte{3}, 32)
	a, err := DatabaseKey(kek)
	must(t, err)
	b, _ := DatabaseKey(kek)
	if len(a) != 32 || !bytes.Equal(a, b) || bytes.Equal(a, kek) {
		t.Fatal("deterministic, 32 bytes, different from the key it comes from")
	}
	other, _ := DatabaseKey(bytes.Repeat([]byte{4}, 32))
	if bytes.Equal(a, other) {
		t.Fatal("two instance keys, one database key")
	}
	if _, err := DatabaseKey([]byte("short")); err == nil {
		t.Fatal("a key of the wrong size")
	}
}
