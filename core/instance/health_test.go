package instance

import (
	"testing"
	"time"

	"kedalio/core/offsite"
)

func TestOffsiteHealthIsOkOnlyWithARecentCopyOnTheStore(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	const every = 24 * time.Hour
	cases := []struct {
		name string
		st   offsite.State
		want string
	}{
		{"not tried yet", offsite.State{}, "waiting"},
		{"keys work, the store has no copy", offsite.State{LastOK: now}, "stale"},
		{"keys work, the newest copy is old", offsite.State{LastOK: now, NewestCopy: now.Add(-2*every - time.Minute)}, "stale"},
		{"a recent copy", offsite.State{LastOK: now, NewestCopy: now.Add(-every)}, "ok"},
		{"the store refuses", offsite.State{LastOK: now, NewestCopy: now, FailingSince: now}, "failing"},
	}
	for _, c := range cases {
		if got := offsiteHealth(c.st, every, now)["offsite_backup"]; got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}
