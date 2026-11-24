package instance

import (
	"context"
	"testing"
	"time"

	"kedalio/core/memory"
)

// startWith opens the instance, reads what it settled on and closes it again.
func startWith(t *testing.T, o Options) (lang, zone string, areas memory.Areas) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	inst, err := o.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop()
		if err := inst.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	return inst.Language, inst.Zone, inst.store.Areas
}

func TestNewDatabaseDefaultsToEnglishAndTheHostZone(t *testing.T) {
	t.Setenv("TZ", "Asia/Tokyo")
	o := testOptions(t.TempDir())
	o.Language = ""
	lang, zone, areas := startWith(t, o)
	if lang != "English" || zone != "Asia/Tokyo" || areas != memory.AreasEN {
		t.Fatalf("got %q %q %v", lang, zone, areas)
	}
	// stored: a later start under another TZ and no flags keeps what the database was made with
	t.Setenv("TZ", "America/New_York")
	if lang, zone, _ = startWith(t, o); lang != "English" || zone != "Asia/Tokyo" {
		t.Fatalf("second start: %q %q", lang, zone)
	}
}

func TestNewDatabaseTakesTheChosenLanguageAndZone(t *testing.T) {
	o := testOptions(t.TempDir())
	o.Language, o.Zone = "Italian", "Europe/Rome"
	lang, zone, areas := startWith(t, o)
	if lang != "Italian" || zone != "Europe/Rome" || areas != memory.AreasIT {
		t.Fatalf("got %q %q %v", lang, zone, areas)
	}
	o.Language, o.Zone = "", ""
	if lang, zone, areas = startWith(t, o); lang != "Italian" || zone != "Europe/Rome" || areas != memory.AreasIT {
		t.Fatalf("reopened: %q %q %v", lang, zone, areas)
	}
}

// forgetLocale puts a database back as the code before 1.0 left it: the memory's tables, nothing about language or zone.
func forgetLocale(t *testing.T, o Options) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	inst, err := o.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inst.DB.Exec("delete from memory_meta where k in ('language', 'zone')"); err != nil {
		t.Fatal(err)
	}
	stop()
	if err := inst.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExplicitZoneOverridesTheStoredOne(t *testing.T) {
	o := testOptions(t.TempDir())
	o.Zone = "Europe/Rome"
	startWith(t, o)
	o.Zone = "Asia/Tokyo"
	if _, zone, _ := startWith(t, o); zone != "Asia/Tokyo" {
		t.Fatalf("override: %q", zone)
	}
	o.Zone = ""
	if _, zone, _ := startWith(t, o); zone != "Europe/Rome" {
		t.Fatalf("the override is for that run only: %q", zone)
	}
}

func TestUnknownZoneIsRefused(t *testing.T) {
	o := testOptions(t.TempDir())
	o.Zone = "Mars/Olympus"
	if _, err := o.Start(context.Background()); err == nil {
		t.Fatal("started on an unknown zone")
	}
}

func TestHostZoneNameReadsTZThenTheSystem(t *testing.T) {
	t.Setenv("TZ", "Europe/Paris")
	if got := hostZoneName(); got != "Europe/Paris" {
		t.Fatalf("TZ: %q", got)
	}
	t.Setenv("TZ", "")
	got := hostZoneName()
	if _, err := time.LoadLocation(got); err != nil || got == "" || got == "Local" {
		t.Fatalf("system: %q %v", got, err)
	}
}
