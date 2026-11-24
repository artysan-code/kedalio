package instance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirstRunFileHoldsTheRecoveryCodeWithMode0600(t *testing.T) {
	dir := t.TempDir()
	// a file an earlier run left, world-readable
	if err := os.WriteFile(filepath.Join(dir, firstRunFile), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFirstRun(dir)
	if err := f.setRecovery("RECOVERY-1"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "RECOVERY-1") || strings.Contains(string(b), "old") || strings.Contains(string(b), "link") {
		t.Errorf("the file: %s", b)
	}
	if st, _ := os.Stat(f.path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
	// a restart before the owner copied the code keeps it (it is shown once)
	g := newFirstRun(dir)
	if err := g.setRecovery(g.recovery); err != nil {
		t.Fatal(err)
	}
	if b, _ = os.ReadFile(g.path); !strings.Contains(string(b), "RECOVERY-1") {
		t.Errorf("the file after a restart: %s", b)
	}
	g.remove()
	if _, err := os.Stat(g.path); !os.IsNotExist(err) {
		t.Errorf("remove left the file: %v", err)
	}
}

// The setup link is gone (ADR 0007): a file an older core wrote with one is rewritten without it, keeping the code,
// which the owner may not have copied yet; a file that held only the link has nothing left to keep.
func TestAnOldFileWithASetupLinkIsRewrittenWithoutItAndKeepsTheRecoveryCode(t *testing.T) {
	dir := t.TempDir()
	old := "Kedalio first-run secrets.\n\nRecovery code: RECOVERY-1\n\nOpen this link in a browser to make your first passkey.\n" +
		"Setup link:    https://core.example.com/setup#LINK-1\n"
	path := filepath.Join(dir, firstRunFile)
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	newFirstRun(dir).dropOldLink()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the file went, and with it the only copy of the recovery code: %v", err)
	}
	if !strings.Contains(string(b), "RECOVERY-1") || strings.Contains(string(b), "LINK-1") || strings.Contains(strings.ToLower(string(b)), "setup link") {
		t.Errorf("the file: %s", b)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
	// a file with no link is left alone
	before, _ := os.ReadFile(path)
	newFirstRun(dir).dropOldLink()
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf("a file with no link was rewritten: %s", after)
	}
	// the link and no code (the code was printed instead): nothing is left
	dir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir2, firstRunFile), []byte("Setup link:    https://core.example.com/setup#LINK-2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	newFirstRun(dir2).dropOldLink()
	if _, err := os.Stat(filepath.Join(dir2, firstRunFile)); !os.IsNotExist(err) {
		t.Errorf("a file with no secret left stayed: %v", err)
	}
}

func TestStartWithAPublicURLWritesNoSetupLinkAndCleansTheOneAnOlderCoreLeft(t *testing.T) {
	o := testOptions(t.TempDir())
	o.PublicURL = "https://core.example.com"
	code := started(t, o)
	path := filepath.Join(o.Data, firstRunFile)
	if b, err := os.ReadFile(path); err != nil || !strings.Contains(string(b), code) || strings.Contains(strings.ToLower(string(b)), "link") {
		t.Fatalf("first start: %v %s", err, b)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("Setup link:    https://core.example.com/setup#LINK-1\n")
	f.Close()
	ctx, stop := context.WithCancel(context.Background())
	inst, err := o.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stop()
	inst.Close()
	if b, err := os.ReadFile(path); err != nil || !strings.Contains(string(b), code) || strings.Contains(string(b), "LINK-1") {
		t.Errorf("second start: %v %s", err, b)
	}
}

func TestStartPrintsTheRecoveryCodeWhenTheFileCannotBeWrittenAndDoesNotLoseIt(t *testing.T) {
	dir := t.TempDir()
	// a directory where the file goes: the rename fails whoever runs the test
	if err := os.Mkdir(filepath.Join(dir, firstRunFile), 0o700); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	code := started(t, testOptions(dir))
	os.Stderr = old
	w.Close()
	var seen strings.Builder
	buf := make([]byte, 4096)
	for n, _ := r.Read(buf); n > 0; n, _ = r.Read(buf) {
		seen.Write(buf[:n])
	}
	if !strings.Contains(seen.String(), code) {
		t.Errorf("the code was neither in a file nor printed: %s", seen.String())
	}
}

func TestStartKeepsTheRecoveryCodeOutOfTheTerminalAndInTheFirstRunFile(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	dir := t.TempDir()
	code := started(t, testOptions(dir))
	os.Stderr = old
	w.Close()
	var seen strings.Builder
	buf := make([]byte, 4096)
	for n, _ := r.Read(buf); n > 0; n, _ = r.Read(buf) {
		seen.Write(buf[:n])
	}
	if code == "" {
		t.Fatal("the helper did not return the code")
	}
	if strings.Contains(seen.String(), code) {
		t.Errorf("the recovery code reached stderr: %s", seen.String())
	}
	b, err := os.ReadFile(filepath.Join(dir, firstRunFile))
	if err != nil || !strings.Contains(string(b), code) {
		t.Errorf("first-run file: %v %s", err, b)
	}
}
