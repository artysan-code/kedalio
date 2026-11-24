package db

import "testing"

func TestNormalizeS3PrefixEndsInASlashUnlessEmpty(t *testing.T) {
	for in, want := range map[string]string{"": "", "kedalio-core": "kedalio-core/", "kedalio-core/": "kedalio-core/", "a/b": "a/b/"} {
		if got := NormalizeS3Prefix(in); got != want {
			t.Errorf("NormalizeS3Prefix(%q) = %q, want %q", in, got, want)
		}
	}
}
