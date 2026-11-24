package tasks

import (
	"fmt"
	"regexp"
	"time"
	// The zone database travels with the binary: a host without one still gives the owner's days.
	_ "time/tzdata"
)

// LoadZone returns the zone of an IANA name, the host's zone for "".
func LoadZone(name string) (*time.Location, error) {
	if name == "" {
		return time.Local, nil
	}
	return time.LoadLocation(name)
}

// Only turning an instant into a day or an hour needs the zone; counting days on the calendar (AddDays, NextDate) is
// done at UTC, where every day has 24 hours.

// DayOf is the day of an instant in the zone.
func DayOf(t time.Time, loc *time.Location) string { return t.In(loc).Format("2006-01-02") }

// HHMM is the hour and minute of an instant in the zone.
func HHMM(t time.Time, loc *time.Location) string { return t.In(loc).Format("15:04") }

func parseDay(day string) time.Time {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		panic(fmt.Sprintf("not a day: %s", day))
	}
	return t
}

// AddDays is a day plus n days, as a day.
func AddDays(day string, n int) string {
	return parseDay(day).AddDate(0, 0, n).Format("2006-01-02")
}

var (
	dayRe  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	timeRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
)

// ValidDay reports whether s is a real calendar day, YYYY-MM-DD.
func ValidDay(s string) bool {
	if !dayRe.MatchString(s) {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// ValidTime reports whether s is HH:MM, 24 hours.
func ValidTime(s string) bool { return timeRe.MatchString(s) }

// stamp is how a task page writes an instant.
func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func parseStamp(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	return t, err == nil
}
