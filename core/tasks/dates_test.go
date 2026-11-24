package tasks

import (
	"testing"
	"time"
)

func TestDaysAndHoursAreTheZonesNotTheMachines(t *testing.T) {
	// 23:30 UTC on 31 March is already 1 April in Rome (summer time, +2) and still 31 March in New York
	instant := time.Date(2026, 3, 31, 23, 30, 0, 0, time.UTC)
	rome, err := LoadZone("Europe/Rome")
	if err != nil {
		t.Fatal(err)
	}
	ny, err := LoadZone("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, DayOf(instant, rome), "2026-04-01")
	eq(t, HHMM(instant, rome), "01:30")
	eq(t, MomentOf(instant, rome), "morning")
	eq(t, DayOf(instant, ny), "2026-03-31")
	eq(t, HHMM(instant, ny), "19:30")
	eq(t, MomentOf(instant, ny), "evening")
	// the calendar does not depend on the zone: the day summer time starts still has its next day
	eq(t, AddDays("2026-03-29", 1), "2026-03-30")
	if !ValidDay("2024-02-29") || ValidDay("2026-02-29") || ValidDay("2026-2-9") {
		t.Error("ValidDay")
	}
	if !ValidTime("09:05") || ValidTime("25:00") || ValidTime("9:05") {
		t.Error("ValidTime")
	}
}

func TestTheDefaultZoneIsTheHostsAndAnUnknownOneIsRefused(t *testing.T) {
	z, err := LoadZone("")
	if err != nil || z != time.Local {
		t.Errorf("%v, %v", z, err)
	}
	if _, err := LoadZone("Mars/Olympus"); err == nil {
		t.Error("an unknown zone is refused")
	}
}
