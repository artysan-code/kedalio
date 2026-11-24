package tasks

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Tasks that stopped moving, with the cause. Stalled is pure over rows of the task index; the
// loaders below read them. A quiet but healthy task is the price of the rules: a finding can be snoozed «until a day»,
// which ends early when the task sees activity.

// StallCode is why a task stalled.
type StallCode string

// The reasons a task is called stalled.
const (
	DoingIdle      StallCode = "doing_idle"      // doing, nothing written for N days
	WaitingPast    StallCode = "waiting_past"    // waiting, and the day it waits for has passed
	WaitingNobody  StallCode = "waiting_nobody"  // waiting, and nobody or nothing named
	BlockedDropped StallCode = "blocked_dropped" // waits for a task that was dropped
	BlockedMissing StallCode = "blocked_missing" // waits for a task that does not exist
	BlockedCycle   StallCode = "blocked_cycle"   // waits, through others, for itself
	Overdue        StallCode = "overdue"         // todo, past its day by more than N days
)

// Stall is one reason.
type Stall struct {
	Code StallCode `json:"code"`
	// Days is how long: days idle, days past the date.
	Days int `json:"days,omitempty"`
	// On is what it concerns: the blocker's id, the day waited for.
	On string `json:"on,omitempty"`
}

// StalledTask is a task that stalled, with its reasons.
type StalledTask struct {
	ID      string  `json:"id"`
	Title   string  `json:"title"`
	Status  Status  `json:"status"`
	Due     string  `json:"due,omitempty"`
	Project string  `json:"project,omitempty"`
	Reasons []Stall `json:"reasons"`
}

// StallRow is one task as the rules see it: a row of the task index, its blockers, and when it last saw activity.
type StallRow struct {
	ID      string
	Title   string
	Status  Status
	Due     string
	Project string
	// WorkedBy is `run:<id>`, `engine:claude`, a person…, as the index keeps it (lower case).
	WorkedBy string
	// WaitingOn is a person, `run:<id>`, a day or a task id, as the index keeps it.
	WaitingOn string
	// BlockedBy are the ids it waits for (lower case).
	BlockedBy []string
	Updated   string
	// Activity is the latest of Updated and the writes to its log; Updated when unset.
	Activity  string
	Snooze    string
	SnoozedAt string
}

// Thresholds say how long before a task is called stalled.
type Thresholds struct {
	// DoingDays: a task `doing` with no write for this many days.
	DoingDays int `json:"doingDays"`
	// TodoDays: a `todo` past its day by more than this many days.
	TodoDays int `json:"todoDays"`
}

// DefaultThresholds are a week and a fortnight.
var DefaultThresholds = Thresholds{DoingDays: 7, TodoDays: 14}

// isOpen is a task that is neither done nor dropped; withWaiting says whether a waiting one counts (the day's lists
// keep it apart from the ones to do, a stall check counts it).
func isOpen(s Status, withWaiting bool) bool {
	return s == Todo || s == Doing || (withWaiting && s == Waiting)
}

// dayDiff is the whole days from one day to the other.
func dayDiff(from, to string) int { return daysSince(to) - daysSince(from) }

// Stalled is the open tasks that stalled at now, each with its reasons, most reasons first.
func Stalled(rows []StallRow, now time.Time, th Thresholds, loc *time.Location) []StalledTask {
	today := DayOf(now, loc)
	byID := map[string]StallRow{}
	for _, r := range rows {
		byID[strings.ToLower(r.ID)] = r
	}
	cyclic := func(r StallRow) bool {
		// a path from the task back to itself through blockers still open (a done blocker releases)
		seen := map[string]bool{}
		var walk func(id string) bool
		walk = func(id string) bool {
			for _, b := range byID[id].BlockedBy {
				if b == strings.ToLower(r.ID) {
					return true
				}
				x, ok := byID[b]
				if !ok || !isOpen(x.Status, true) || seen[b] {
					continue
				}
				seen[b] = true
				if walk(b) {
					return true
				}
			}
			return false
		}
		return walk(strings.ToLower(r.ID))
	}
	var out []StalledTask
	for _, r := range rows {
		if !isOpen(r.Status, true) {
			continue
		}
		activity := r.Updated
		if r.Activity != "" && r.Activity > r.Updated {
			activity = r.Activity
		}
		// «looked at, fine until a day»: ends with the day, or with any activity after it was set
		if r.Snooze != "" && r.Snooze >= today && !(r.SnoozedAt != "" && activity > r.SnoozedAt) {
			continue
		}
		var why []Stall
		idle := 0
		if at, ok := parseStamp(activity); ok {
			idle = int(math.Floor(now.Sub(at).Hours() / 24))
		}
		if r.Status == Doing && idle >= th.DoingDays {
			why = append(why, Stall{Code: DoingIdle, Days: idle})
		}
		if r.Status == Waiting {
			day := r.Due
			if dayRe.MatchString(r.WaitingOn) {
				day = r.WaitingOn
			}
			if day != "" && day < today {
				why = append(why, Stall{Code: WaitingPast, On: day, Days: dayDiff(day, today)})
			}
			if r.WaitingOn == "" {
				why = append(why, Stall{Code: WaitingNobody})
			}
		}
		if r.Status == Todo && r.Due != "" && dayDiff(r.Due, today) > th.TodoDays {
			why = append(why, Stall{Code: Overdue, On: r.Due, Days: dayDiff(r.Due, today)})
		}
		for _, b := range r.BlockedBy {
			x, ok := byID[b]
			if !ok {
				why = append(why, Stall{Code: BlockedMissing, On: b})
			} else if x.Status == Dropped {
				why = append(why, Stall{Code: BlockedDropped, On: x.ID})
			}
		}
		if len(r.BlockedBy) > 0 && cyclic(r) {
			why = append(why, Stall{Code: BlockedCycle})
		}
		if len(why) > 0 {
			out = append(out, StalledTask{ID: r.ID, Title: r.Title, Status: r.Status, Due: r.Due, Project: r.Project, Reasons: why})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i].Reasons) != len(out[j].Reasons) {
			return len(out[i].Reasons) > len(out[j].Reasons)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ---------------------------------------------------------------- the account's data

// LoadThresholds are the account's thresholds: its settings, else the defaults.
func LoadThresholds(ctx context.Context, sqldb *sql.DB) (Thresholds, error) {
	n := func(k string, d int) (int, error) {
		v, err := setting(ctx, sqldb, k)
		if err != nil {
			return 0, err
		}
		if i, convErr := strconv.Atoi(v); convErr == nil && i >= 1 && i <= 365 {
			return i, nil
		}
		return d, nil
	}
	var th Thresholds
	var err error
	if th.DoingDays, err = n("stalled_doing_days", DefaultThresholds.DoingDays); err != nil {
		return th, err
	}
	th.TodoDays, err = n("stalled_todo_days", DefaultThresholds.TodoDays)
	return th, err
}

// SetThresholds writes the thresholds given (0 leaves one as it is). The error says which is not acceptable.
func SetThresholds(ctx context.Context, sqldb *sql.DB, doingDays, todoDays int) error {
	given := []struct {
		key, name string
		days      int
	}{{"stalled_doing_days", "doingDays", doingDays}, {"stalled_todo_days", "todoDays", todoDays}}
	for _, e := range given {
		if e.days != 0 && (e.days < 1 || e.days > 365) {
			return refusef("%s is a whole number of days, 1 to 365", e.name)
		}
	}
	for _, e := range given {
		if e.days == 0 {
			continue
		}
		if err := putSetting(ctx, sqldb, e.key, e.days); err != nil {
			return err
		}
	}
	return nil
}

// stallRows is the index as rows for Stalled: every task (a blocker may be closed), the writes to a task's log counted
// as its activity, and the snooze its page carries.
func stallRows(ctx context.Context, sqldb *sql.DB) ([]StallRow, error) {
	blockers := map[string][]string{}
	rels, err := sqldb.QueryContext(ctx, "select id, key from task_rels where rel = 'blocked_by' and kind = 'task'")
	if err != nil {
		return nil, err
	}
	for rels.Next() {
		var id, key string
		if err := rels.Scan(&id, &key); err != nil {
			rels.Close()
			return nil, err
		}
		blockers[id] = append(blockers[id], key)
	}
	rels.Close()
	// the log lives beside the task (`tasks/<id>/log`): a line there is activity on the task
	logged := map[string]string{}
	logs, err := sqldb.QueryContext(ctx, "select path, updated from docs where path like 'tasks/t-%/%' and deleted = 0")
	if err != nil {
		return nil, err
	}
	for logs.Next() {
		var path, updated string
		if err := logs.Scan(&path, &updated); err != nil {
			logs.Close()
			return nil, err
		}
		id := strings.Split(path, "/")[1]
		if updated > logged[id] {
			logged[id] = updated
		}
	}
	logs.Close()
	type snooze struct{ at, since string }
	snoozed := map[string]snooze{}
	bodies, err := sqldb.QueryContext(ctx, "select body from docs where path like 'tasks/t-%' and deleted = 0 and body like '%snooze:%'")
	if err != nil {
		return nil, err
	}
	for bodies.Next() {
		var body string
		if err := bodies.Scan(&body); err != nil {
			bodies.Close()
			return nil, err
		}
		if t := FromFile(body, ""); t != nil && t.Snooze != "" {
			snoozed[t.ID] = snooze{t.Snooze, t.SnoozedAt}
		}
	}
	bodies.Close()
	cur, err := sqldb.QueryContext(ctx, "select id, title, status, due, project, worked_by, waiting_on, updated from task_index")
	if err != nil {
		return nil, err
	}
	defer cur.Close()
	var out []StallRow
	for cur.Next() {
		var r StallRow
		var status string
		var due, project, workedBy, waitingOn, updated sql.NullString
		if err := cur.Scan(&r.ID, &r.Title, &status, &due, &project, &workedBy, &waitingOn, &updated); err != nil {
			return nil, err
		}
		r.Status, r.Due, r.Project, r.WorkedBy, r.WaitingOn, r.Updated =
			Status(status), due.String, project.String, workedBy.String, waitingOn.String, updated.String
		r.BlockedBy = blockers[r.ID]
		r.Activity = logged[r.ID]
		if s, ok := snoozed[r.ID]; ok {
			r.Snooze, r.SnoozedAt = s.at, s.since
		}
		out = append(out, r)
	}
	return out, cur.Err()
}

// ReasonText is a finding as one line for a person to read, in English; the app and the report put their own words
// around the codes.
func ReasonText(s Stall) string {
	switch s.Code {
	case DoingIdle:
		return fmt.Sprintf("doing, nothing written for %d days", s.Days)
	case WaitingPast:
		return fmt.Sprintf("waiting; %s has passed (%d days)", s.On, s.Days)
	case WaitingNobody:
		return "waiting, but for nobody or nothing named"
	case BlockedDropped:
		return fmt.Sprintf("waits for %s, which was dropped", s.On)
	case BlockedMissing:
		return fmt.Sprintf("waits for %s, which does not exist", s.On)
	case BlockedCycle:
		return "waits, through other tasks, for itself"
	case Overdue:
		return fmt.Sprintf("%d days past %s", s.Days, s.On)
	}
	return string(s.Code)
}
