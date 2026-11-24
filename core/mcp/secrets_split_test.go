package mcp

import (
	"strings"
	"testing"

	"kedalio/core/vault"
)

// A secret finished by the text a write adds is a new secret, though the line it began on was there before.
func TestWritesRefuseASecretSplitOverAnOldLineAndANewOne(t *testing.T) {
	r, _ := upkeepRig(t, false)
	pem := "-----BEGIN KEY-----\nMIIEvQIBADANBgkq\n-----END KEY-----"
	r.srv.cfg.Mask = func(s string) string {
		return vault.MaskField("", strings.ReplaceAll(s, pem, vault.Hidden))
	}
	log := "tasks/t-20261007-bbbbbb/log.md"
	for _, c := range []struct{ name, old, next string }{
		{"a Bearer and its token", "# Plan\n\n## Log\n- 10:00 Authorization: Bearer\n", "# Plan\n\n## Log\n- 10:00 Authorization: Bearer\nabcdefghijklmnop1234\n"},
		{"a value over two writes", "# Plan\n\n## Log\n-----BEGIN KEY-----\nMIIEvQIBADANBgkq\n", "# Plan\n\n## Log\n" + pem + "\n"},
	} {
		if _, err := r.store.Write(ctx, log, c.old, "owner"); err != nil {
			t.Fatal(err)
		}
		d, _ := r.store.Get(ctx, log)
		out, failed := call(t, r.upkeepSession(t), "memory_write", map[string]any{"op": "write", "path": log, "base_rev": d.Rev, "body": c.next})
		if !failed && out["refused"] != true {
			t.Errorf("%s was taken: %v", c.name, out)
		}
	}
}

// Notes that held a secret from before the scan stay editable, but a second one is refused.
func TestTasksSaveNotesThatHoldAnOldSecretTakeNoNewOne(t *testing.T) {
	r := newRig(t, nil)
	s := r.session(t)
	id := taskID(mustCall(t, s, "tasks_save", map[string]any{"title": "Old router", "notes": "The password is swordfish."}))
	r.srv.cfg.Mask = swordfishMask
	if out, failed := call(t, s, "tasks_save", map[string]any{"id": id, "notes": "The password is swordfish.\nCheck the panel."}); failed {
		t.Fatalf("a harmless addition was refused: %v", out)
	}
	if out, failed := call(t, s, "tasks_save", map[string]any{"id": id, "notes": "The password is swordfish.\nAnd swordfish again."}); !failed {
		t.Errorf("a second secret was taken: %v", out)
	}
}

// Swapping the old secret for a new one on a changed line keeps the count, and is still a new secret.
func TestWritesRefuseASwapOfAnOldSecretForANewOne(t *testing.T) {
	r, _ := upkeepRig(t, false)
	r.srv.cfg.Mask = swordfishMask
	log := "tasks/t-20261007-cccccc/log.md"
	if _, err := r.store.Write(ctx, log, "# Plan\n\n## Log\n- 10:00 password swordfish\n- 11:00 Done\n", "owner"); err != nil {
		t.Fatal(err)
	}
	d, _ := r.store.Get(ctx, log)
	swap := "# Plan\n\n## Log\n- 10:00 token ghp_abcdefghijklmnop1234\n- 11:00 Done\n"
	if out, failed := call(t, r.session(t), "memory_write", map[string]any{"op": "write", "path": log, "base_rev": d.Rev, "body": swap}); !failed && out["refused"] != true {
		t.Errorf("a swapped secret was taken: %v", out)
	}
}

func TestTasksSaveNotesRefuseASwapOfAnOldSecret(t *testing.T) {
	r := newRig(t, nil)
	s := r.session(t)
	id := taskID(mustCall(t, s, "tasks_save", map[string]any{"title": "Old router", "notes": "The password is swordfish."}))
	r.srv.cfg.Mask = swordfishMask
	if out, failed := call(t, s, "tasks_save", map[string]any{"id": id, "notes": "The token is ghp_abcdefghijklmnop1234."}); !failed {
		t.Errorf("a swapped secret was taken: %v", out)
	}
}
