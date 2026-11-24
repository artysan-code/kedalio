package vault

import (
	"path/filepath"
	"strings"
	"testing"

	"kedalio/core/db"
)

// The vault restores from what the owner keeps: the daily copy of the database and the recovery code shown once. A new
// volume with only those two opens the vault again; without the code it stays locked, and a wrong code is refused.
func TestRestoreTheDailyCopyAndTheRecoveryCodeBringTheSecretsBackOnANewVolume(t *testing.T) {
	old, fresh := t.TempDir(), t.TempDir()
	d, err := db.Open(filepath.Join(old, "core.db"))
	must(t, err)
	must(t, Migrate(ctx, d))
	a, err := Open(ctx, d, old)
	must(t, err)
	code, _ := a.TakeRecoveryCode(ctx)
	if code == "" {
		t.Fatal("the code is shown once, at the first sign-in")
	}
	seedSecret(t, d, a, "FORGE_TOKEN", "the-forge-token")
	seedSecret(t, d, a, "SMTP", "the-smtp-password")
	// the daily copy: a consistent file made by SQLite itself
	_, err = d.ExecContext(ctx, "vacuum into ?", filepath.Join(fresh, "core.db"))
	must(t, err)
	d.Close()

	// the volume is lost: only the copy (kept elsewhere) and the code are left
	nd, err := db.Open(filepath.Join(fresh, "core.db"))
	must(t, err)
	defer nd.Close()
	b, err := Open(ctx, nd, fresh)
	must(t, err)
	if !b.Locked() {
		t.Fatal("an instance that had a key is never given a new one")
	}
	if b.Mask("the-forge-token") != "the-forge-token" {
		t.Fatal("locked: the secrets do not open")
	}
	if ok, _ := b.Unlock(ctx, "AAAA-AAAA-AAAA-AAAA-AAAA-AAAA-AAAA-AAAA-AAAA-AAAA-AAAA-AAAA-AAAA"); ok {
		t.Fatal("a wrong code")
	}
	if ok, err := b.Unlock(ctx, strings.ToLower(strings.ReplaceAll(code, "-", " "))); !ok || err != nil {
		t.Fatalf("the code: %v %v", ok, err)
	}
	if got := b.Mask("the-forge-token the-smtp-password"); got != Hidden+" "+Hidden {
		t.Fatalf("the secrets are back: %q", got)
	}
}
