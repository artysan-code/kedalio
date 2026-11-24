package upkeep

import (
	"fmt"
	"sort"
	"time"
)

// WeekAge is how many days a week must be over before its days are summarised.
const WeekAge = 7

// Day is a diary day as DueWeeks sees it.
type Day struct {
	Path       string
	Day        string // YYYY-MM-DD
	HasLines   bool   // the day holds at least one list line
	Summarised bool   // its front matter has `summarised:`
}

// Week is an ISO week with the days still to fold into its page.
type Week struct {
	Key  string // 2026-W42
	End  string // its Sunday, YYYY-MM-DD
	Days []Day  // oldest first
}

// ISOWeek is the key (2026-W42) and the Sunday of the ISO week of day (YYYY-MM-DD).
func ISOWeek(day string) (key, end string, err error) {
	t, err := time.Parse(time.DateOnly, day)
	if err != nil {
		return "", "", err
	}
	y, w := t.ISOWeek()
	sunday := t.AddDate(0, 0, (7-int(t.Weekday()))%7)
	return fmt.Sprintf("%d-W%02d", y, w), sunday.Format(time.DateOnly), nil
}

// DueWeeks are the weeks whose Sunday is at least WeekAge days before today and that have days with lines and no
// `summarised:`, the oldest first, at most max (all when max <= 0). Days that are empty, already summarised or not a
// date are ignored.
func DueWeeks(days []Day, today time.Time, max int) []Week {
	last := today.UTC().AddDate(0, 0, -WeekAge).Format(time.DateOnly)
	by := map[string]*Week{}
	for _, d := range days {
		if d.Summarised || !d.HasLines {
			continue
		}
		key, end, err := ISOWeek(d.Day)
		if err != nil || end > last {
			continue
		}
		w := by[key]
		if w == nil {
			w = &Week{Key: key, End: end}
			by[key] = w
		}
		w.Days = append(w.Days, d)
	}
	out := make([]Week, 0, len(by))
	for _, w := range by {
		sort.Slice(w.Days, func(i, j int) bool { return w.Days[i].Day < w.Days[j].Day })
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].End < out[j].End })
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out
}
