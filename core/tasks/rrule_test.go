package tasks

import (
	"reflect"
	"strings"
	"testing"
)

func rule(t *testing.T, text string, from ...string) Rule {
	t.Helper()
	f := "due"
	if len(from) > 0 {
		f = from[0]
	}
	r, err := ParseRule(text, f)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// next is NextDate with "" for no next day.
func next(t *testing.T, text, due, done string, from ...string) string {
	t.Helper()
	n, _, err := NextDate(rule(t, text, from...), due, done)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func ruleError(text string) string {
	_, err := ParseRule(text, "due")
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestParseRuleTheSupportedPartsCaseAndPrefix(t *testing.T) {
	r := rule(t, "RRULE:freq=monthly;interval=2;byday=1mo,-1fr;until=2027-12-31")
	if r.Freq != "MONTHLY" || r.Interval != 2 || r.Until != "2027-12-31" {
		t.Errorf("%+v", r)
	}
	if want := []ByDay{{1, 1}, {5, -1}}; !reflect.DeepEqual(r.ByDay, want) {
		t.Errorf("byday %v", r.ByDay)
	}
	if got := rule(t, "FREQ=MONTHLY;BYMONTHDAY=1,-1").ByMonthDay; !reflect.DeepEqual(got, []int{1, -1}) {
		t.Errorf("bymonthday %v", got)
	}
	if got := rule(t, "FREQ=DAILY;UNTIL=20271231T000000Z").Until; got != "2027-12-31" {
		t.Errorf("until %q", got)
	}
}

func TestFormatRuleRoundTrips(t *testing.T) {
	for _, in := range []string{"FREQ=DAILY", "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,FR", "FREQ=MONTHLY;BYDAY=-1FR;UNTIL=20271231"} {
		if got := FormatRule(rule(t, in)); got != in {
			t.Errorf("%q -> %q", in, got)
		}
	}
}

func TestParseRuleRefusalsSayWhy(t *testing.T) {
	for in, want := range map[string]string{
		"FREQ=DAILY;COUNT=3;UNTIL=20271231": "COUNT and UNTIL",
		"FREQ=DAILY;COUNT=3":                "COUNT is not supported",
		"FREQ=HOURLY":                       "not supported",
		"INTERVAL=2":                        "FREQ is required",
		"FREQ=DAILY;BYSETPOS=1":             "BYSETPOS is not supported",
		"FREQ=DAILY;INTERVAL=0":             "INTERVAL",
		"FREQ=DAILY;INTERVAL=x":             "INTERVAL",
		"FREQ=WEEKLY;BYDAY=1MO":             "MONTHLY only",
		"FREQ=DAILY;BYDAY=MO":               "BYDAY",
		"FREQ=MONTHLY;BYDAY=6MO":            "ordinal",
		"FREQ=MONTHLY;BYDAY=XX":             "BYDAY",
		"FREQ=WEEKLY;BYMONTHDAY=1":          "MONTHLY only",
		"FREQ=MONTHLY;BYMONTHDAY=0":         "BYMONTHDAY",
		"FREQ=MONTHLY;BYMONTHDAY=32":        "BYMONTHDAY",
		"FREQ=DAILY;UNTIL=20271331":         "UNTIL",
		"FREQ=DAILY;FREQ=DAILY":             "twice",
		"FREQ=DAILY;WKST=SU":                "WKST",
		"garbage":                           "NAME=VALUE",
	} {
		if got := ruleError(in); !strings.Contains(got, want) {
			t.Errorf("%q: %q does not say %q", in, got, want)
		}
	}
	if _, err := ParseRule("FREQ=DAILY", "never"); err == nil {
		t.Error("a bad `from` is refused")
	}
}

func TestNextDateDailyAndInterval(t *testing.T) {
	for _, c := range [][3]string{
		{"FREQ=DAILY", "2026-10-26", "2026-10-27"},
		{"FREQ=DAILY;INTERVAL=3", "2026-10-30", "2026-11-02"},
		{"FREQ=DAILY", "2026-12-31", "2027-01-01"},
		{"FREQ=DAILY", "2028-02-28", "2028-02-29"},
		{"FREQ=DAILY", "2027-02-28", "2027-03-01"},
	} {
		if got := next(t, c[0], c[1], ""); got != c[2] {
			t.Errorf("%s from %s = %s, want %s", c[0], c[1], got, c[2])
		}
	}
}

func TestNextDateWeekly(t *testing.T) {
	for _, c := range [][3]string{
		{"FREQ=WEEKLY", "2026-10-26", "2026-11-02"},
		{"FREQ=WEEKLY;BYDAY=MO,WE,FR", "2026-10-26", "2026-10-28"},
		{"FREQ=WEEKLY;BYDAY=MO,WE,FR", "2026-10-30", "2026-11-02"},
		{"FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR", "2026-10-30", "2026-11-02"},
		// every 2 weeks: Monday 26 Oct, then Monday 9 Nov; the same week's later day comes first
		{"FREQ=WEEKLY;INTERVAL=2;BYDAY=MO", "2026-10-26", "2026-11-09"},
		{"FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,FR", "2026-10-26", "2026-10-30"},
		{"FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,FR", "2026-10-30", "2026-11-09"},
		// a Sunday anchor belongs to the week that began on Monday (WKST=MO)
		{"FREQ=WEEKLY;BYDAY=SU,MO", "2026-10-25", "2026-10-26"},
	} {
		if got := next(t, c[0], c[1], ""); got != c[2] {
			t.Errorf("%s from %s = %s, want %s", c[0], c[1], got, c[2])
		}
	}
}

func TestNextDateMonthlySkipsMonthsWithoutTheDay(t *testing.T) {
	for _, c := range [][3]string{
		{"FREQ=MONTHLY", "2026-10-15", "2026-11-15"},
		{"FREQ=MONTHLY", "2026-12-15", "2027-01-15"},
		{"FREQ=MONTHLY", "2026-01-31", "2026-03-31"},
		{"FREQ=MONTHLY", "2026-03-31", "2026-05-31"},
		{"FREQ=MONTHLY", "2028-01-30", "2028-03-30"},
		{"FREQ=MONTHLY", "2028-01-29", "2028-02-29"},
		{"FREQ=MONTHLY", "2027-01-29", "2027-03-29"},
		{"FREQ=MONTHLY;INTERVAL=3", "2026-11-30", "2027-05-30"},
	} {
		if got := next(t, c[0], c[1], ""); got != c[2] {
			t.Errorf("%s from %s = %s, want %s", c[0], c[1], got, c[2])
		}
	}
}

func TestNextDateMonthlyByMonthDay(t *testing.T) {
	for _, c := range [][3]string{
		{"FREQ=MONTHLY;BYMONTHDAY=-1", "2026-01-31", "2026-02-28"},
		{"FREQ=MONTHLY;BYMONTHDAY=-1", "2028-01-31", "2028-02-29"},
		{"FREQ=MONTHLY;BYMONTHDAY=-1", "2026-02-28", "2026-03-31"},
		{"FREQ=MONTHLY;BYMONTHDAY=-2", "2026-04-01", "2026-04-29"},
		{"FREQ=MONTHLY;BYMONTHDAY=1,15", "2026-10-01", "2026-10-15"},
		{"FREQ=MONTHLY;BYMONTHDAY=1,15", "2026-10-15", "2026-11-01"},
		{"FREQ=MONTHLY;BYMONTHDAY=31", "2026-01-31", "2026-03-31"},
		{"FREQ=MONTHLY;BYMONTHDAY=29", "2027-01-29", "2027-03-29"},
	} {
		if got := next(t, c[0], c[1], ""); got != c[2] {
			t.Errorf("%s from %s = %s, want %s", c[0], c[1], got, c[2])
		}
	}
}

func TestNextDateMonthlyByDayWithOrdinals(t *testing.T) {
	for _, c := range [][3]string{
		// 1 Oct 2026 is a Thursday: first Monday of October is the 5th
		{"FREQ=MONTHLY;BYDAY=1MO", "2026-09-30", "2026-10-05"},
		{"FREQ=MONTHLY;BYDAY=1MO", "2026-10-05", "2026-11-02"},
		{"FREQ=MONTHLY;BYDAY=-1FR", "2026-10-01", "2026-10-30"},
		{"FREQ=MONTHLY;BYDAY=-1FR", "2026-10-30", "2026-11-27"},
		{"FREQ=MONTHLY;BYDAY=-2FR", "2026-10-01", "2026-10-23"},
		{"FREQ=MONTHLY;BYDAY=2TU,4TU", "2026-10-13", "2026-10-27"},
		{"FREQ=MONTHLY;BYDAY=5MO", "2026-10-01", "2026-11-30"},
		{"FREQ=MONTHLY;BYDAY=5MO", "2026-11-30", "2027-03-29"},
		{"FREQ=MONTHLY;BYDAY=-1FR", "2028-02-01", "2028-02-25"},
		// without an ordinal: every such weekday of the month
		{"FREQ=MONTHLY;BYDAY=MO", "2026-10-26", "2026-11-02"},
		{"FREQ=MONTHLY;BYDAY=MO", "2026-10-05", "2026-10-12"},
		// both parts: the days that satisfy both (a Friday the 13th)
		{"FREQ=MONTHLY;BYDAY=FR;BYMONTHDAY=13", "2026-10-01", "2026-11-13"},
		{"FREQ=MONTHLY;BYDAY=FR;BYMONTHDAY=13", "2026-11-13", "2027-08-13"},
	} {
		if got := next(t, c[0], c[1], ""); got != c[2] {
			t.Errorf("%s from %s = %s, want %s", c[0], c[1], got, c[2])
		}
	}
}

func TestNextDateYearlyLeapDayOnlyInLeapYears(t *testing.T) {
	for _, c := range [][3]string{
		{"FREQ=YEARLY", "2026-10-26", "2027-10-26"},
		{"FREQ=YEARLY", "2028-02-29", "2032-02-29"},
		{"FREQ=YEARLY", "2096-02-29", "2104-02-29"}, // 2100 is not a leap year
		{"FREQ=YEARLY;INTERVAL=2", "2026-03-01", "2028-03-01"},
	} {
		if got := next(t, c[0], c[1], ""); got != c[2] {
			t.Errorf("%s from %s = %s, want %s", c[0], c[1], got, c[2])
		}
	}
}

func TestNextDateFromDueOrFromDone(t *testing.T) {
	for _, c := range []struct{ rule, due, done, from, want string }{
		{"FREQ=WEEKLY", "2026-10-26", "2026-10-26", "due", "2026-11-02"},
		{"FREQ=DAILY;INTERVAL=3", "2026-10-30", "2026-10-28", "due", "2026-11-02"},
		// done late, from due: never lands in the past
		{"FREQ=WEEKLY", "2026-10-12", "2026-10-28", "due", "2026-11-02"},
		{"FREQ=DAILY", "2026-10-12", "2026-10-28", "due", "2026-10-29"},
		{"FREQ=DAILY;INTERVAL=7", "2000-01-01", "2026-10-28", "due", "2026-10-31"}, // 9797 days on, jumped over
		{"FREQ=DAILY;INTERVAL=3", "2026-10-12", "2026-10-28", "done", "2026-10-31"},
		{"FREQ=WEEKLY", "2026-10-12", "2026-10-28", "done", "2026-11-04"},
		{"FREQ=MONTHLY", "2026-10-12", "2026-01-31", "done", "2026-03-31"},
		// from done without a done day falls back to due
		{"FREQ=DAILY", "2026-10-12", "", "done", "2026-10-13"},
	} {
		if got := next(t, c.rule, c.due, c.done, c.from); got != c.want {
			t.Errorf("%s due %s done %s from %s = %s, want %s", c.rule, c.due, c.done, c.from, got, c.want)
		}
	}
}

func TestNextDateUntilIsInclusiveThenNothing(t *testing.T) {
	for _, c := range [][4]string{
		{"FREQ=DAILY;UNTIL=20261027", "2026-10-26", "", "2026-10-27"},
		{"FREQ=DAILY;UNTIL=20261027", "2026-10-27", "", ""},
		{"FREQ=DAILY;UNTIL=20261026", "2026-10-26", "", ""},
		{"FREQ=WEEKLY;UNTIL=20261101", "2026-10-26", "", ""},
		{"FREQ=WEEKLY;UNTIL=20261102", "2026-10-26", "", "2026-11-02"},
		{"FREQ=MONTHLY;BYDAY=-1FR;UNTIL=20261029", "2026-10-01", "", ""},
		{"FREQ=DAILY;UNTIL=20261031", "2026-10-12", "2026-11-05", ""}, // done after the end
	} {
		if got := next(t, c[0], c[1], c[2]); got != c[3] {
			t.Errorf("%s from %s = %q, want %q", c[0], c[1], got, c[3])
		}
	}
}

func TestNextDateARuleThatNeverFallsReturnsNothingAndBadDaysFail(t *testing.T) {
	if got := next(t, "FREQ=MONTHLY;BYDAY=FR;BYMONTHDAY=31;UNTIL=20271231", "2026-10-01", ""); got != "2027-12-31" {
		t.Errorf("got %q", got)
	}
	if got := next(t, "FREQ=MONTHLY;BYDAY=1MO;BYMONTHDAY=30", "2026-10-01", ""); got != "" {
		t.Errorf("got %q", got)
	}
	if _, _, err := NextDate(rule(t, "FREQ=DAILY"), "2026-02-30", ""); err == nil {
		t.Error("a bad day is an error")
	}
}

func TestLegacyWordsMapToRulesAndKeepTheirRhythm(t *testing.T) {
	for word, want := range map[string]string{
		"daily": "FREQ=DAILY", "weekdays": "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR", "weekly": "FREQ=WEEKLY", "monthly": "FREQ=MONTHLY",
	} {
		if got := FormatRule(FromLegacy(word)); got != want {
			t.Errorf("%s -> %s", word, got)
		}
	}
	for _, c := range [][3]string{{"weekdays", "2026-10-30", "2026-11-02"}, {"weekly", "2026-10-26", "2026-11-02"}, {"monthly", "2026-10-26", "2026-11-26"}} {
		if n, _, _ := NextDate(FromLegacy(c[0]), c[1], ""); n != c[2] {
			t.Errorf("%s from %s = %s", c[0], c[1], n)
		}
	}
	if FromLegacy("daily").From != "due" {
		t.Error("legacy rules count from the due day")
	}
}

func TestRuleForAStringTheOldWordsAndTheOldMonthlyOnTheLastDays(t *testing.T) {
	r := func(repeat, due string) string {
		p, err := RuleFor(repeat, "", due)
		if err != nil {
			return err.Error()
		}
		return FormatRule(p)
	}
	for _, c := range [][3]string{
		{"FREQ=WEEKLY;INTERVAL=2;BYDAY=MO", "2026-10-26", "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO"},
		{"weekdays", "2026-10-26", "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR"},
		{"monthly", "2026-10-28", "FREQ=MONTHLY"},
		{"monthly", "2026-01-31", "FREQ=MONTHLY;BYMONTHDAY=-1"},
	} {
		if got := r(c[0], c[1]); got != c[2] {
			t.Errorf("%s on %s = %s", c[0], c[1], got)
		}
	}
	if !strings.Contains(r("FREQ=HOURLY", "2026-10-26"), "not supported") {
		t.Error("a bad rule is refused")
	}
	m, _ := RuleFor("monthly", "", "2026-01-31")
	if n, _, _ := NextDate(m, "2026-01-31", ""); n != "2026-02-28" {
		t.Errorf("February is not skipped: %s", n)
	}
	if n, _, _ := NextDate(m, "2026-02-28", ""); n != "2026-03-31" {
		t.Errorf("%s", n)
	}
}
