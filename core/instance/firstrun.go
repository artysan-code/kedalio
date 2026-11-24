package instance

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// firstRunFile is the file in the data folder that holds what the owner is shown once, at the first start: the recovery
// code. (An older core also wrote the link that made the first passkey; a passkey is now made at /setup with an
// approval code, ADR 0007, and the link is dropped from a file that still has it.)
const firstRunFile = "first-run.txt"

const (
	recoveryLabel = "Recovery code: "
	// oldLinkLabel starts the line an older core wrote; it is read only to be removed.
	oldLinkLabel = "Setup link:"
)

// firstRun keeps that file. The secret is not printed: in a container the terminal is the container's log, which is
// kept and shipped, so it goes to a file only the core's user can read, and the log names the file. The owner deletes
// the file after copying the recovery code out.
type firstRun struct {
	path string
	mu   sync.Mutex
	told bool
	// stdio: no passkey exists in this mode, and the code is what opens the database if the key file is lost
	stdio    bool
	recovery string
	// hadLink: the file an earlier start left has the setup link of an older core
	hadLink bool
}

// newFirstRun reads what an earlier start left, so a restart before the owner copied the recovery code (shown once)
// does not lose it.
func newFirstRun(data string) *firstRun {
	f := &firstRun{path: filepath.Join(data, firstRunFile)}
	if b, err := os.ReadFile(f.path); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, recoveryLabel); ok {
				f.recovery = strings.TrimSpace(v)
			}
			if strings.HasPrefix(l, oldLinkLabel) {
				f.hadLink = true
			}
		}
	}
	return f
}

// dropOldLink rewrites a file that still has an older core's setup link without it, keeping the recovery code: it is
// shown once and the owner may not have copied it yet. A file with nothing else in it is removed.
func (f *firstRun) dropOldLink() {
	if !f.hadLink {
		return
	}
	if f.recovery == "" {
		f.remove()
		return
	}
	f.mu.Lock()
	f.told = true // the owner was told about the file when it was first written
	f.mu.Unlock()
	if err := f.setRecovery(f.recovery); err != nil {
		slog.Warn("first-run.txt could not be rewritten without the setup link", "file", f.path, "err", err)
	}
}

// setRecovery records the recovery code and rewrites the file whole.
func (f *firstRun) setRecovery(code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recovery = code
	text := "Kedalio first-run secret. Delete this file once you have copied the recovery code.\n" +
		"\nThe recovery code is the only way to read the backups after a lost disk. Write it down and keep it away from\nthis machine; it is shown once, here.\n" +
		recoveryLabel + code + "\n"
	if f.stdio {
		text += "\nThere is no passkey in stdio mode. If the key file (kek, in this folder) is lost or the folder is moved without it,\nthe recovery code is what unlocks the database.\n"
	}
	// write through a fresh file: one an earlier run left may have a looser mode, and a mode is never widened
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, f.path); err != nil {
		return err
	}
	if !f.told && f.stdio {
		f.told = true
		fmt.Fprintf(os.Stderr, "kedalio-core: the recovery code is in %s: copy it somewhere safe, then delete the file. It unlocks the "+
			"database if the key file is lost.\n", f.path)
	}
	if !f.told {
		f.told = true
		fmt.Fprintf(os.Stderr, "kedalio-core: the recovery code is in %s: read it, copy it somewhere safe, then delete the file.\n", f.path)
	}
	return nil
}

// remove deletes the file; a missing one is fine.
func (f *firstRun) remove() { _ = os.Remove(f.path) }
