package tasks

import (
	"sort"
	"strings"
	"time"
)

// What the day holds, seen from an account's day.

func prio(t Task) int {
	if t.Priority == 0 {
		return 2
	}
	return t.Priority
}

func timeKey(t Task) string {
	if t.Time == "" {
		return "99:99"
	}
	return t.Time
}

func byTimeThenPriority(a, b Task) int {
	if c := strings.Compare(timeKey(a), timeKey(b)); c != 0 {
		return c
	}
	if prio(a) != prio(b) {
		return prio(a) - prio(b)
	}
	return strings.Compare(a.Title, b.Title)
}

func sortTasks(ts []Task, cmp func(a, b Task) int) []Task {
	sort.SliceStable(ts, func(i, j int) bool { return cmp(ts[i], ts[j]) < 0 })
	return ts
}

func byDueThenTime(a, b Task) int {
	if c := strings.Compare(a.Due, b.Due); c != 0 {
		return c
	}
	return byTimeThenPriority(a, b)
}

// Moment is the part of the day.
type Moment string

// MomentOf is the part of the day at now in the zone.
func MomentOf(now time.Time, loc *time.Location) Moment {
	switch h := now.In(loc).Hour(); {
	case h < 13:
		return "morning"
	case h < 18:
		return "afternoon"
	}
	return "evening"
}

// ForOther reports whether a task is for a person other than the account.
func ForOther(t Task, me string) bool {
	f := ForOf(t)
	return f != nil && !isMe(f.Target, me)
}

// AssignedToOther reports whether the task is assigned to a person other than the account.
func AssignedToOther(t Task, me string) bool {
	a := AssignedOf(t)
	return a != nil && !isMe(a.Target, me)
}

// WorkedByRun reports whether a run is working on the task (an engine or agent without a run does not take it from
// the owner).
func WorkedByRun(t Task) bool {
	w := WorkedByOf(t)
	return w != nil && w.Kind == KindRun
}

// DayBrief is what the day holds.
type DayBrief struct {
	Day    string
	Moment Moment
	// Overdue is what is past its day, oldest first.
	Overdue []Task
	// Missed are timed tasks already past this hour, still open: missed today (a past appointment is just past).
	Missed []Task
	// Today is the rest of today still ahead.
	Today    []Task
	Tomorrow []Task
	Upcoming []Task
	Undated  []Task
	// Waiting is a task waited on, assigned to another person, or paused.
	Waiting []Task
	// Agents are the tasks a run works on.
	Agents []Task
	// ForOthers are the tasks for another person, listed apart.
	ForOthers []Task
	DoneToday int
}

// upcomingDays is how far ahead `Upcoming` looks: three days from today.
const upcomingDays = 3

// Brief is what the day holds, seen from now in the zone. In the evening `Tomorrow` is what comes next. «Yours» is what
// is for the account, done by it or by no one in particular (an engine or agent that works for it included), not
// waited on and not in the hands of a run.
func Brief(tasks []Task, now time.Time, me string, loc *time.Location) DayBrief {
	today, clock := DayOf(now, loc), HHMM(now, loc)
	others := func(t Task) bool { return ForOther(t, me) }
	var mine []Task
	for _, t := range tasks {
		if isOpen(t.Status, false) && !others(t) && !AssignedToOther(t, me) && WaitingOnOf(t) == nil && !WorkedByRun(t) {
			mine = append(mine, t)
		}
	}
	pick := func(from []Task, keep func(Task) bool) []Task {
		out := []Task{}
		for _, t := range from {
			if keep(t) {
				out = append(out, t)
			}
		}
		return out
	}
	todayAll := sortTasks(pick(mine, func(t Task) bool { return t.Due == today }), byTimeThenPriority)
	b := DayBrief{
		Day:     today,
		Moment:  MomentOf(now, loc),
		Overdue: sortTasks(pick(mine, func(t Task) bool { return t.Due != "" && t.Due < today }), byDueThenTime),
		Missed:  pick(todayAll, func(t Task) bool { return t.Time != "" && t.Time < clock }),
		Today:   pick(todayAll, func(t Task) bool { return t.Time == "" || t.Time >= clock }),
		Tomorrow: sortTasks(pick(mine, func(t Task) bool { return t.Due == AddDays(today, 1) }),
			byTimeThenPriority),
		Upcoming: sortTasks(pick(mine, func(t Task) bool {
			return t.Due != "" && t.Due > AddDays(today, 1) && t.Due <= AddDays(today, upcomingDays)
		}), byDueThenTime),
		Undated: sortTasks(pick(mine, func(t Task) bool { return t.Due == "" }), byTimeThenPriority),
		Waiting: pick(tasks, func(t Task) bool {
			return !others(t) && !WorkedByRun(t) &&
				(t.Status == Waiting || (isOpen(t.Status, false) && (AssignedToOther(t, me) || WaitingOnOf(t) != nil)))
		}),
		Agents: pick(tasks, func(t Task) bool {
			return isOpen(t.Status, true) && !others(t) && WorkedByRun(t)
		}),
		ForOthers: pick(tasks, func(t Task) bool { return isOpen(t.Status, true) && others(t) }),
	}
	for _, t := range tasks {
		if t.Status == Done && t.Done != "" {
			if d, ok := parseStamp(t.Done); ok && DayOf(d, loc) == today {
				b.DoneToday++
			}
		}
	}
	return b
}
