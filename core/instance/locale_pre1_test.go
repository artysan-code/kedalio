package instance

import (
	"context"
	"testing"

	"kedalio/core/memory"
)

func TestPre1DatabaseRunsWithWhatTheOldCodeDidAndRecordsIt(t *testing.T) {
	for _, c := range []struct {
		flag, lang string
		areas      memory.Areas
	}{{"", "English", memory.AreasEN}, {"Italian", "Italian", memory.AreasIT}} {
		t.Setenv("TZ", "UTC")
		o := testOptions(t.TempDir())
		o.Language = c.flag
		startWith(t, o)
		forgetLocale(t, o)
		lang, zone, areas := startWith(t, o)
		if lang != c.lang || zone != "Europe/Rome" || areas != c.areas {
			t.Fatalf("flag %q: got %q %q %v", c.flag, lang, zone, areas)
		}
		// recorded: a later start does not depend on the code's defaults or on the flag
		o.Language = ""
		if lang, zone, areas = startWith(t, o); lang != c.lang || zone != "Europe/Rome" || areas != c.areas {
			t.Fatalf("flag %q, later: got %q %q %v", c.flag, lang, zone, areas)
		}
	}
}

func TestPre1DatabaseTakesAnAskedZoneAndKeepsItsLanguageAgainstAnotherFlag(t *testing.T) {
	o := testOptions(t.TempDir())
	o.Language = "Italian"
	startWith(t, o)
	forgetLocale(t, o)
	o.Zone = "Asia/Tokyo"
	if lang, zone, _ := startWith(t, o); lang != "Italian" || zone != "Asia/Tokyo" {
		t.Fatalf("got %q %q", lang, zone)
	}
	o.Language, o.Zone = "English", ""
	if lang, zone, areas := startWith(t, o); lang != "Italian" || zone != "Asia/Tokyo" || areas != memory.AreasIT {
		t.Fatalf("got %q %q %v", lang, zone, areas)
	}
}

func TestStoredLocaleReadsTheDatabaseFlagsOverrideAndNothingIsWritten(t *testing.T) {
	o := testOptions(t.TempDir())
	o.Language, o.Zone = "Italian", "Asia/Tokyo"
	startWith(t, o)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	inst, err := o.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	for _, c := range []struct{ lang, zone, wantLang, wantZone string }{
		{"", "", "Italian", "Asia/Tokyo"}, {"English", "UTC", "English", "UTC"},
	} {
		l, z, err := StoredLocale(ctx, inst.DB, c.lang, c.zone)
		if err != nil || l != c.wantLang || z != c.wantZone {
			t.Fatalf("%v: got %q %q %v", c, l, z, err)
		}
	}
	if _, err := inst.DB.Exec("delete from memory_meta where k in ('language', 'zone')"); err != nil {
		t.Fatal(err)
	}
	if l, z, err := StoredLocale(ctx, inst.DB, "", ""); err != nil || l != "English" || z != "Europe/Rome" {
		t.Fatalf("nothing stored: %q %q %v", l, z, err)
	}
	var n int
	if err := inst.DB.QueryRow("select count(*) from memory_meta where k in ('language', 'zone')").Scan(&n); err != nil || n != 0 {
		t.Fatalf("StoredLocale wrote %d rows %v", n, err)
	}
}

func TestBadZoneOnAFirstStartWritesNothingAndTheNextStartIsStillNew(t *testing.T) {
	t.Setenv("TZ", "UTC")
	o := testOptions(t.TempDir())
	o.Zone = "Mars/Olympus"
	if _, err := o.Start(context.Background()); err == nil {
		t.Fatal("started on an unknown zone")
	}
	o.Zone = ""
	// the host's zone, not the legacy one: the failed start left no data, so the database is still new
	if _, zone, _ := startWith(t, o); zone != "UTC" {
		t.Fatalf("zone %q", zone)
	}
}
