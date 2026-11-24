package main

import (
	"bytes"
	"testing"
)

func TestPrintVersionAnswersTheVersionFlagsOnly(t *testing.T) {
	old := version
	version = "1.2.3"
	t.Cleanup(func() { version = old })
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"-version"}, true},
		{[]string{"--version"}, true},
		{nil, false},
		{[]string{"clients"}, false},
		{[]string{"-version", "x"}, false},
	} {
		var out bytes.Buffer
		if got := printVersion(tc.args, &out); got != tc.want {
			t.Errorf("%v: handled = %v, want %v", tc.args, got, tc.want)
		}
		wantOut := ""
		if tc.want {
			wantOut = "kedalio-core 1.2.3\n"
		}
		if out.String() != wantOut {
			t.Errorf("%v: printed %q, want %q", tc.args, out.String(), wantOut)
		}
	}
}
