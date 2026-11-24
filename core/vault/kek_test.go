package vault

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRecoveryCodeIsTheKeysBytesInGroupsOfFourReadBackWhateverTheDashesAndCase(t *testing.T) {
	raw := make([]byte, 32)
	rand.Read(raw)
	code := RecoveryCode(raw)
	if !regexp.MustCompile(`^([A-Z2-7]{4}-){12}[A-Z2-7]{4}$`).MatchString(code) {
		t.Fatalf("code %q", code)
	}
	if got := fromRecoveryCode(code); !bytes.Equal(got, raw) {
		t.Fatalf("read back %x", got)
	}
	if got := fromRecoveryCode(strings.ToLower(strings.ReplaceAll(code, "-", " "))); !bytes.Equal(got, raw) {
		t.Fatalf("read back from lower case %x", got)
	}
	if fromRecoveryCode("ABCD-1234") != nil || fromRecoveryCode("not base32 !") != nil {
		t.Fatal("what is not a code is not read")
	}
}

func TestKEKIsMadeOnceOwnerOnlyTheCodeShownOnceALostFileLocksItsCodeUnlocks(t *testing.T) {
	d, dir := newDB(t), t.TempDir()
	first, err := Open(ctx, d, dir)
	if err != nil || first.Locked() {
		t.Fatalf("first start: %v locked=%v", err, first.Locked())
	}
	file := filepath.Join(dir, "kek")
	st, err := os.Stat(file)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", st, err)
	}
	again, err := Open(ctx, d, dir)
	if err != nil || again.Locked() || again.KeyID() != first.KeyID() {
		t.Fatalf("the same key on the next start: %v", err)
	}
	sealed := again.kek.wrap([]byte{1, 2, 3}, "aad")
	if got, err := again.kek.unwrap(sealed, "aad"); err != nil || !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Fatalf("unwrap: %v %v", got, err)
	}
	if _, err := again.kek.unwrap(sealed, "other"); err == nil {
		t.Fatal("bound to its associated data")
	}

	code, err := first.TakeRecoveryCode(ctx)
	if err != nil || code == "" {
		t.Fatalf("code: %q %v", code, err)
	}
	if code2, _ := first.TakeRecoveryCode(ctx); code2 != "" {
		t.Fatal("the code is shown once")
	}

	os.Remove(file)
	lost, err := Open(ctx, d, dir)
	if err != nil || !lost.Locked() {
		t.Fatalf("never a new key for an instance that had one: %v locked=%v", err, lost.Locked())
	}
	if _, err := os.Stat(file); err == nil {
		t.Fatal("no new key file")
	}
	if ok, err := lost.Unlock(ctx, RecoveryCode(make([]byte, 32))); ok || err != nil {
		t.Fatalf("another key is refused: %v %v", ok, err)
	}
	if ok, err := lost.Unlock(ctx, code); !ok || err != nil {
		t.Fatalf("the code unlocks: %v %v", ok, err)
	}
	if lost.Locked() || lost.KeyID() != first.KeyID() {
		t.Fatal("the same key is back")
	}
	if got, err := lost.kek.unwrap(sealed, "aad"); err != nil || !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Fatalf("unwrap after unlock: %v %v", got, err)
	}
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the file is put back, owner-only: %v %v", st, err)
	}
	if _, err := os.Stat(file + ".new"); err == nil {
		t.Fatal("the swap leaves no copy beside the key")
	}

	os.WriteFile(file, make([]byte, 32), 0o600)
	if _, err := Open(ctx, d, dir); err == nil || !strings.Contains(err.Error(), "not this instance's key") {
		t.Fatalf("a file that is not this instance's key is refused: %v", err)
	}
}
