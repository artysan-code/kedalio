package main

import (
	"bytes"
	"strings"
	"testing"

	"kedalio/core/vault"
)

func TestRecoveryKeyTellsMissingFromMalformed(t *testing.T) {
	if _, err := recoveryKey("  "); err == nil || !strings.Contains(err.Error(), "is needed") {
		t.Fatalf("a missing code: %v", err)
	}
	if _, err := recoveryKey("not-a-code"); err == nil || !strings.Contains(err.Error(), "not a valid recovery code") {
		t.Fatalf("a malformed code: %v", err)
	}
	raw := bytes.Repeat([]byte{7}, 32)
	got, err := recoveryKey(vault.RecoveryCode(raw))
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("a valid code: %v", err)
	}
}
