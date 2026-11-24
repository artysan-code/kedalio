package tasks

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A recurrence rule in an iCal RRULE subset, and the next day it falls on. Days are local calendar days
// (YYYY-MM-DD); a task's time of day stays on the task and is not touched here.

// ByDay is a weekday of a BYDAY entry: 1 = Monday … 7 = Sunday; N is the ordinal of a monthly rule (1MO, -1FR), 0 for
// every such weekday.
type ByDay struct{ Day, N int }

// Rule is a parsed recurrence rule.
type Rule struct {
	Freq     string // DAILY, WEEKLY, MONTHLY, YEARLY
	Interval int
	ByDay    []ByDay
	// ByMonthDay are day numbers of the month; negative counts from the end (-1 is the last day).
	ByMonthDay []int
	// Until is the last day a task may fall on, inclusive.
	Until string
	// From is what the next day counts from, as Todoist's `every` ("due") and `every!` ("done").
	From string
}

var (
	dayNames  = []string{"MO", "TU", "WE", "TH", "FR", "SA", "SU"}
	freqNames = []string{"DAILY", "WEEKLY", "MONTHLY", "YEARLY"}
	bydayRe   = regexp.MustCompile(`(?i)^([+-]?\d{1,2})?(MO|TU|WE|TH|FR|SA|SU)$`)
	digitsRe  = regexp.MustCompile(`^\d+$`)
	mdayRe    = regexp.MustCompile(`^[+-]?\d{1,2}$`)
	untilRe   = regexp.MustCompile(`^(\d{4})-?(\d{2})-?(\d{2})(T\d{6}Z?)?$`)
	rruleHead = regexp.MustCompile(`(?i)^RRULE:`)
)

// ParseRule parses `FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,FR;UNTIL=20271231` (an `RRULE:` prefix is accepted). The error
// says what is wrong.
func ParseRule(text, from string) (Rule, error) {
	if from == "" {
		from = "due"
	}
	if from != "due" && from != "done" {
		return Rule{}, refusef(`from must be "due" or "done", got "%s"`, from)
	}
	parts := map[string]string{}
	var order []string
	for _, part := range strings.Split(rruleHead.ReplaceAllString(strings.TrimSpace(text), ""), ";") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		eq := strings.Index(part, "=")
		if eq < 1 {
			return Rule{}, refusef(`"%s" is not NAME=VALUE`, part)
		}
		name := strings.ToUpper(strings.TrimSpace(part[:eq]))
		if _, ok := parts[name]; ok {
			return Rule{}, refusef("%s appears twice", name)
		}
		parts[name] = strings.TrimSpace(part[eq+1:])
		order = append(order, name)
	}
	if _, ok := parts["COUNT"]; ok {
		if _, both := parts["UNTIL"]; both {
			return Rule{}, refuse("COUNT and UNTIL must not appear in the same rule (RFC 5545); COUNT is not supported, use UNTIL")
		}
		return Rule{}, refuse("COUNT is not supported: end a rule with UNTIL")
	}
	for _, name := range order {
		if !slices.Contains([]string{"FREQ", "INTERVAL", "BYDAY", "BYMONTHDAY", "UNTIL", "WKST"}, name) {
			return Rule{}, refusef("%s is not supported (FREQ, INTERVAL, BYDAY, BYMONTHDAY, UNTIL are)", name)
		}
	}
	if w, ok := parts["WKST"]; ok && strings.ToUpper(w) != "MO" {
		return Rule{}, refuse("only WKST=MO is supported")
	}
	freq := strings.ToUpper(parts["FREQ"])
	if freq == "" {
		return Rule{}, refuse("FREQ is required")
	}
	if !slices.Contains(freqNames, freq) {
		return Rule{}, refusef("FREQ=%s is not supported (DAILY, WEEKLY, MONTHLY, YEARLY are)", freq)
	}

	rule := Rule{Freq: freq, Interval: 1, From: from}
	if v, ok := parts["INTERVAL"]; ok {
		n, _ := strconv.Atoi(v)
		if !digitsRe.MatchString(v) || n < 1 || n > 1000 {
			return Rule{}, refusef(`INTERVAL must be 1 to 1000, got "%s"`, v)
		}
		rule.Interval = n
	}

	if v, ok := parts["BYDAY"]; ok {
		rule.ByDay = []ByDay{}
		for _, item := range strings.Split(v, ",") {
			m := bydayRe.FindStringSubmatch(strings.TrimSpace(item))
			if m == nil {
				return Rule{}, refusef(`BYDAY entry "%s" is not like MO or 1MO or -1FR`, item)
			}
			day := slices.Index(dayNames, strings.ToUpper(m[2])) + 1
			if m[1] == "" {
				rule.ByDay = append(rule.ByDay, ByDay{Day: day})
				continue
			}
			n, _ := strconv.Atoi(m[1])
			if n == 0 || n > 5 || n < -5 {
				return Rule{}, refusef(`BYDAY ordinal must be 1 to 5 or -1 to -5, got "%s"`, item)
			}
			rule.ByDay = append(rule.ByDay, ByDay{Day: day, N: n})
		}
		if freq == "DAILY" || freq == "YEARLY" {
			return Rule{}, refusef("BYDAY is not supported with FREQ=%s", freq)
		}
		if freq == "WEEKLY" {
			for _, b := range rule.ByDay {
				if b.N != 0 {
					return Rule{}, refuse("BYDAY with an ordinal (1MO, -1FR) is for FREQ=MONTHLY only")
				}
			}
		}
	}

	if v, ok := parts["BYMONTHDAY"]; ok {
		if freq != "MONTHLY" {
			return Rule{}, refuse("BYMONTHDAY is for FREQ=MONTHLY only")
		}
		rule.ByMonthDay = []int{}
		for _, item := range strings.Split(v, ",") {
			n, _ := strconv.Atoi(strings.TrimSpace(item))
			if !mdayRe.MatchString(strings.TrimSpace(item)) || n == 0 || n > 31 || n < -31 {
				return Rule{}, refusef(`BYMONTHDAY entry "%s" must be 1 to 31 or -1 to -31`, item)
			}
			rule.ByMonthDay = append(rule.ByMonthDay, n)
		}
	}

	if v, ok := parts["UNTIL"]; ok {
		// a date, or a date-time whose day is taken as it is written (the rule works in local days)
		m := untilRe.FindStringSubmatch(v)
		if m == nil || !ValidDay(m[1]+"-"+m[2]+"-"+m[3]) {
			return Rule{}, refusef(`UNTIL must be a date like 20271231, got "%s"`, v)
		}
		rule.Until = m[1] + "-" + m[2] + "-" + m[3]
	}
	return rule, nil
}

// FormatRule is the rule as text, in the form ParseRule reads (From is stored beside it, not in it).
func FormatRule(r Rule) string {
	out := []string{"FREQ=" + r.Freq}
	if r.Interval != 1 {
		out = append(out, "INTERVAL="+strconv.Itoa(r.Interval))
	}
	if len(r.ByDay) > 0 {
		var ds []string
		for _, b := range r.ByDay {
			n := ""
			if b.N != 0 {
				n = strconv.Itoa(b.N)
			}
			ds = append(ds, n+dayNames[b.Day-1])
		}
		out = append(out, "BYDAY="+strings.Join(ds, ","))
	}
	if len(r.ByMonthDay) > 0 {
		var ds []string
		for _, n := range r.ByMonthDay {
			ds = append(ds, strconv.Itoa(n))
		}
		out = append(out, "BYMONTHDAY="+strings.Join(ds, ","))
	}
	if r.Until != "" {
		out = append(out, "UNTIL="+strings.ReplaceAll(r.Until, "-", ""))
	}
	return strings.Join(out, ";")
}

var legacyWords = []string{"daily", "weekdays", "weekly", "monthly"}

// FromLegacy is the rule one of the four words the tasks carried before rules meant. `monthly` once clamped 31 Jan to
// the last day of February; a rule skips months without the day (RFC 5545), so a task on the 29th to 31st now skips
// them: the migration keeps the rule's meaning, not the clamp.
func FromLegacy(repeat string) Rule {
	base := Rule{Interval: 1, From: "due"}
	switch repeat {
	case "daily":
		base.Freq = "DAILY"
	case "weekdays":
		base.Freq = "WEEKLY"
		for d := 1; d <= 5; d++ {
			base.ByDay = append(base.ByDay, ByDay{Day: d})
		}
	case "weekly":
		base.Freq = "WEEKLY"
	default:
		base.Freq = "MONTHLY"
	}
	return base
}

// RuleFor is the rule a task's `repeat` and `repeat_from` mean on its due day: a rule string, or one of the four old
// words (read by FromLegacy). The old `monthly` on the 29th to 31st went to the month's last day, so it becomes
// `BYMONTHDAY=-1`: a rule alone would skip the months that lack the day.
func RuleFor(repeat, from, due string) (Rule, error) {
	if !slices.Contains(legacyWords, repeat) {
		return ParseRule(repeat, from)
	}
	rule := FromLegacy(repeat)
	if from != "" && from != "due" && from != "done" {
		return Rule{}, refusef(`from must be "due" or "done", got "%s"`, from)
	}
	if repeat == "monthly" && len(due) >= 10 {
		if n, _ := strconv.Atoi(due[8:10]); n >= 29 {
			rule.ByMonthDay = []int{-1}
		}
	}
	if from != "" {
		rule.From = from
	}
	return rule, nil
}

// Calendar arithmetic at UTC, where every day has 24 hours.
func ymd(day string) (y, m, d int) {
	t := parseDay(day)
	return t.Year(), int(t.Month()), t.Day()
}

// iso builds a day; month and day may overflow, the time package carries them.
func iso(y, m, d int) string {
	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
}

func weekday(day string) int {
	w := int(parseDay(day).Weekday())
	if w == 0 {
		return 7
	}
	return w
}

func daysIn(y, m int) int { return time.Date(y, time.Month(m)+1, 0, 0, 0, 0, 0, time.UTC).Day() }

// period is the days of one period of the rule, ascending; start fixes what the rule leaves open (the weekday, the day).
func period(r Rule, start string, k int) []string {
	y, m, d := ymd(start)
	step := k * r.Interval
	switch r.Freq {
	case "DAILY":
		return []string{AddDays(start, step)}
	case "WEEKLY":
		monday := AddDays(start, 7*step-(weekday(start)-1))
		set := map[int]bool{}
		for _, b := range r.ByDay {
			set[b.Day] = true
		}
		if len(r.ByDay) == 0 {
			set[weekday(start)] = true
		}
		var wds []int
		for wd := range set {
			wds = append(wds, wd)
		}
		slices.Sort(wds)
		var out []string
		for _, wd := range wds {
			out = append(out, AddDays(monday, wd-1))
		}
		return out
	case "YEARLY":
		year := y + step
		if d <= daysIn(year, m) { // 29 Feb only in leap years
			return []string{iso(year, m, d)}
		}
		return nil
	}
	first := time.Date(y, time.Month(m-1+step+1), 1, 0, 0, 0, 0, time.UTC)
	year, month := first.Year(), int(first.Month())
	last := daysIn(year, month)
	if len(r.ByDay) == 0 && len(r.ByMonthDay) == 0 {
		if d <= last {
			return []string{iso(year, month, d)}
		}
		return nil
	}
	days := map[int]bool{}
	for _, n := range r.ByMonthDay {
		day := n
		if n < 0 {
			day = last + n + 1
		}
		if day >= 1 && day <= last {
			days[day] = true
		}
	}
	if len(r.ByDay) > 0 {
		byday := map[int]bool{}
		for _, b := range r.ByDay {
			var all []int
			for day := 1; day <= last; day++ {
				if weekday(iso(year, month, day)) == b.Day {
					all = append(all, day)
				}
			}
			if b.N == 0 {
				for _, day := range all {
					byday[day] = true
				}
				continue
			}
			i := b.N - 1 // a 5th Monday that month does not exist: skipped
			if b.N < 0 {
				i = len(all) + b.N
			}
			if i >= 0 && i < len(all) {
				byday[all[i]] = true
			}
		}
		// BYMONTHDAY and BYDAY together: the days that satisfy both (RFC 5545 limits)
		if len(r.ByMonthDay) > 0 {
			both := map[int]bool{}
			for day := range days {
				if byday[day] {
					both[day] = true
				}
			}
			days = both
		} else {
			days = byday
		}
	}
	var ds []int
	for day := range days {
		ds = append(ds, day)
	}
	slices.Sort(ds)
	var out []string
	for _, day := range ds {
		out = append(out, iso(year, month, day))
	}
	return out
}

// maxPeriods guards against a rule that can never fall (a 31st that is also a Monday in no month looked at).
const maxPeriods = 1200 // a century of months

func daysSince(day string) int { return int(parseDay(day).Unix() / 86400) }

// NextDate is the next day the rule falls on, strictly after the anchor: due when the rule counts from "due", done
// when from "done" (due if the task has no done day). A task done late never lands in the past: from "due", the next
// day is also after done. Periods of INTERVAL > 1 count from the anchor. ok is false when UNTIL passed or no day
// exists; the error is for an anchor that is no day.
func NextDate(r Rule, due, done string) (next string, ok bool, err error) {
	for _, day := range []string{due, done} {
		if day != "" && !ValidDay(day) {
			return "", false, refusef("not a day: %s", day)
		}
	}
	start := due
	if r.From == "done" && done != "" {
		start = done
	}
	after := start
	if r.From == "due" && done != "" && done > due {
		after = done
	}
	// jump over the periods that end before `after` (a task done years late), one period of margin
	ay, am, _ := ymd(after)
	sy, sm, _ := ymd(start)
	var units int
	switch r.Freq {
	case "DAILY":
		units = daysSince(after) - daysSince(start)
	case "WEEKLY":
		units = (daysSince(after) - daysSince(start)) / 7
	case "MONTHLY":
		units = (ay-sy)*12 + am - sm
	default:
		units = ay - sy
	}
	k0 := max(0, units/r.Interval-1)
	for k := k0; k < k0+maxPeriods; k++ {
		for _, day := range period(r, start, k) {
			if r.Until != "" && day > r.Until {
				return "", false, nil
			}
			if day > after {
				return day, true, nil
			}
		}
	}
	return "", false, nil
}
