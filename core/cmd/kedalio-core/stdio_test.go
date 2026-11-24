package main

import (
	"path/filepath"
	"testing"
)

func TestDefaultDataDirFollowsThePlatform(t *testing.T) {
	cases := []struct {
		goos, home, config, xdg, want string
		fails                         bool
	}{
		{goos: "linux", home: "/home/alice", want: "/home/alice/.local/share/kedalio"},
		{goos: "linux", home: "/home/alice", xdg: "/data/alice", want: "/data/alice/kedalio"},
		{goos: "linux", home: "/home/alice", xdg: "relative", want: "/home/alice/.local/share/kedalio"},
		{goos: "darwin", home: "/Users/alice", config: "/Users/alice/Library/Application Support", want: "/Users/alice/Library/Application Support/Kedalio"},
		{goos: "windows", config: `C:\Users\alice\AppData\Roaming`, want: filepath.Join(`C:\Users\alice\AppData\Roaming`, "Kedalio")},
		{goos: "linux", fails: true},
		{goos: "darwin", home: "/Users/alice", fails: true},
	}
	for _, c := range cases {
		got, err := defaultDataDir(c.goos, c.home, c.config, c.xdg)
		if (err != nil) != c.fails || got != c.want {
			t.Errorf("%+v: got %q, %v", c, got, err)
		}
	}
}
