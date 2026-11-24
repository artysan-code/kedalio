package mcp

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"kedalio/core/memory"
	"kedalio/core/upkeep"
	"kedalio/protocol"
)

// memory_upkeep: the worklist and the run bookkeeping of the nightly upkeep. The tool holds no model and writes no page
// but the inbox (inbox_placed) and, behind a switch, the week marks; the agent that holds the upkeep token does the
// rewriting with the memory tools, and what it writes is checked as `upkeep` (memory.go).

// Upkeep is what the upkeep tool needs besides the memory and the tasks.
type Upkeep struct {
	DB *sql.DB
	// MarkWeeks turns on the week marking: writing a week page that holds at least three list items sets `summarised:`
	// on the days of that week, which recall then reads as folded into it. Off until the effect on recall is measured.
	MarkWeeks bool
}

// upkeepAuthor is who a write is recorded under when it came with the upkeep token.
const upkeepAuthor = "upkeep"

const (
	maxReport = 2000
	// recentRuns is how many runs op runs answers: three nights of three runs, and one more
	recentRuns = 10
	// the most items of a job one answer carries; hubs went from 5 to 15 when 70 were queued and 5 took 80 s of a
	// run's 700; logs from 6 to 10 with 43 queued
	conflictItems, inboxItems, duplicateItems, hubItems, logItems, weekItems = 10, 15, 15, 15, 10, 3
	// hubWords is how long a page may grow before it is offered to be split
	hubWords = 400
	runCap   = 1 << 30
	// the most keys or pages one call names, and the longest of them
	maxKeys, maxKeyLen = 50, 200
)

// upkeepMay are the tools the upkeep token can call: the worklist, reading, and the writing the no-lost-sentence check
// governs. The token is on a machine that runs a model over the memory, so the rest (the tasks' writes, the sources,
// the settings, the links) is out of its reach.
var upkeepMay = map[string]bool{
	protocol.ToolMemoryUpkeep: true, protocol.ToolMemoryRecall: true, protocol.ToolMemoryRead: true,
	protocol.ToolMemoryList: true, protocol.ToolMemoryWrite: true, protocol.ToolMemoryDelete: true,
	protocol.ToolTasksGet: true, protocol.ToolTasksList: true, protocol.ToolSessionStart: true,
}

const upkeepMayList = "memory_upkeep, memory_recall, memory_read, memory_list, memory_write, memory_delete, tasks_get, tasks_list, session_start"

// jobs are the kinds of work, in the order `next` offers them.
var jobs = []string{"inbox", "conflicts", "duplicates", "hubs", "logs", "weeks"}

var (
	inboxEntryHead = regexp.MustCompile(`^- \d{4}-\d{2}-\d{2} \d{2}:\d{2} ?`)
	listItem       = regexp.MustCompile(`(?m)^\s*[-*]\s+\S`)
	diaryDay       = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

func (s *Server) upkeepTool() *tool {
	return &tool{
		name: protocol.ToolMemoryUpkeep, kind: write,
		description: "Nightly upkeep (upkeep agent; the owner may read worklist and runs). worklist: due per job, or its items with keys. start: open a run (one at a time), answers run id and items. end: close it with status and report. runs: last ten. dismiss: keys of rejected duplicates or conflicts. inbox_placed: drop entries whose text is in kept_in.",
		schema: object([]string{"op"},
			choice("op", []string{"worklist", "start", "end", "runs", "dismiss", "inbox_placed"}),
			choice("job", jobs, "worklist: its items; start: the job; dismiss: duplicates or conflicts"),
			str("run", "end: the run id start gave"),
			choice("status", []string{"ok", "partial", "failed"}, "end"),
			str("report", "end: max 2000 chars"),
			strings_("items", "dismiss, inbox_placed: item keys"),
			strings_("kept_in", "inbox_placed: pages the text went to")),
		run: s.upkeepRun,
	}
}

func (s *Server) upkeepRun(ctx context.Context, raw []byte) (any, error) {
	if s.cfg.Upkeep == nil || s.cfg.Upkeep.DB == nil {
		return nil, refusal("(call)", "the upkeep is not set up on this core", "nothing to do here")
	}
	a, err := decode[protocol.MemoryUpkeep](raw)
	if err != nil {
		return nil, err
	}
	d := s.cfg.Upkeep.DB
	now := time.Now()
	for _, list := range []struct {
		name string
		v    []string
	}{{"items", a.Items}, {"kept_in", a.KeptIn}} {
		if err := capList(list.name, list.v); err != nil {
			return nil, err
		}
	}
	switch a.Op {
	case "worklist":
		if a.Job == "" {
			return s.upkeepCounts(ctx)
		}
		items, err := s.upkeepItems(ctx, a.Job)
		if err != nil {
			return nil, err
		}
		return map[string]any{"job": a.Job, "items": items}, nil
	case "start":
		if err := needs("op start", f("job", a.Job)); err != nil {
			return nil, err
		}
		if !slices.Contains(jobs, a.Job) {
			return nil, refusal("job", "unknown", "use one of: "+strings.Join(jobs, ", "))
		}
		id, err := upkeep.Start(ctx, d, a.Job, now)
		if errors.Is(err, upkeep.ErrOpen) {
			return nil, refusal("run", err.Error(), "end that run (op end with its id), or wait: a run stays open for two hours")
		}
		if err != nil {
			return nil, err
		}
		// a run that cannot go on is closed as failed: left open it would block the next one for two hours
		failed := func(err error) (any, error) {
			msg := err.Error()
			if s.cfg.Mask != nil {
				msg = s.cfg.Mask(msg)
			}
			_ = upkeep.End(ctx, d, id, "failed", "start: "+msg, now)
			return nil, err
		}
		sweep, err := s.cfg.Tasks.Reshape(ctx, 50)
		if err != nil {
			return failed(err)
		}
		items, err := s.upkeepItems(ctx, a.Job)
		if err != nil {
			return failed(err)
		}
		return map[string]any{"run": id, "job": a.Job, "sweep": sweep, "items": items}, nil
	case "end":
		if err := needs("op end", f("run", a.Run), f("status", a.Status)); err != nil {
			return nil, err
		}
		if n := len([]rune(a.Report)); n > maxReport {
			return nil, refusal("report", fmt.Sprintf("%d characters: at most %d", n, maxReport), "say what was done in fewer words")
		}
		report := a.Report
		if s.cfg.Mask != nil {
			report = s.cfg.Mask(report)
		}
		if err := upkeep.End(ctx, d, a.Run, a.Status, report, now); err != nil {
			return nil, refusal("run", err.Error(), "use the id start answered")
		}
		return map[string]any{"ended": a.Run, "status": a.Status}, nil
	case "runs":
		rs, err := upkeep.Recent(ctx, d, recentRuns)
		if err != nil {
			return nil, err
		}
		out := make([]map[string]any, 0, len(rs))
		for _, r := range rs {
			m := map[string]any{"run": r.ID, "job": r.Job, "started": r.Started}
			if r.Ended == "" {
				m["status"] = "open"
			} else {
				m["ended"], m["status"], m["report"] = r.Ended, r.Status, r.Report
			}
			out = append(out, m)
		}
		return map[string]any{"runs": out}, nil
	case "dismiss":
		if a.Job != "duplicates" && a.Job != "conflicts" {
			return nil, refusal("job", "only duplicates and conflicts are dismissed", "give job duplicates or conflicts")
		}
		if len(a.Items) == 0 {
			return nil, refusal("items", "missing", "give the keys of the items to dismiss")
		}
		for _, k := range a.Items {
			if err := upkeep.Dismiss(ctx, d, a.Job, k, now); err != nil {
				return nil, err
			}
		}
		return map[string]any{"dismissed": len(a.Items), "job": a.Job}, nil
	case "inbox_placed":
		return s.inboxPlaced(ctx, a)
	}
	return nil, refusal("op", "unknown", "use worklist, start, end, runs, dismiss or inbox_placed")
}

// capList refuses a list of keys or pages with too many entries or an entry too long.
func capList(name string, v []string) error {
	if len(v) > maxKeys {
		return refusal(name, fmt.Sprintf("%d entries: at most %d", len(v), maxKeys), "call again with fewer")
	}
	for _, k := range v {
		if len(k) > maxKeyLen {
			return refusal(name, fmt.Sprintf("an entry of %d bytes: at most %d", len(k), maxKeyLen), "give the keys or pages as the worklist and the memory name them")
		}
	}
	return nil
}

// upkeepCounts is how much is due per job, and the job the next run should do.
func (s *Server) upkeepCounts(ctx context.Context) (any, error) {
	counts := map[string]int{}
	for _, j := range jobs {
		all, err := s.upkeepAll(ctx, j)
		if err != nil {
			return nil, err
		}
		counts[j] = len(all)
	}
	last, err := upkeep.LastStarted(ctx, s.cfg.Upkeep.DB)
	if err != nil {
		return nil, err
	}
	var n any
	if next := nextJob(counts, last); next != "" {
		n = next
	}
	return map[string]any{"counts": counts, "next": n}, nil
}

// nextJob is, among the jobs with something due, the one whose last run started longest ago (never is longest), in the
// order of jobs on a tie. Taking the first job with items starved the others: the inbox gets entries every day, so it
// won every night while hubs, logs and weeks only grew.
func nextJob(counts map[string]int, lastStarted map[string]string) string {
	next, at := "", ""
	for _, j := range jobs {
		if counts[j] == 0 {
			continue
		}
		t := lastStarted[j]
		if next == "" || t < at {
			next, at = j, t
		}
	}
	return next
}

// upkeepItems is the bounded list of a job's items, oldest first (the longest first for hubs).
func (s *Server) upkeepItems(ctx context.Context, job string) ([]map[string]any, error) {
	all, err := s.upkeepAll(ctx, job)
	if err != nil {
		return nil, err
	}
	max := map[string]int{"conflicts": conflictItems, "inbox": inboxItems, "duplicates": duplicateItems,
		"hubs": hubItems, "logs": logItems, "weeks": weekItems}[job]
	if len(all) > max {
		all = all[:max]
	}
	return all, nil
}

func shortKey(prefix string, parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return prefix + hex.EncodeToString(h[:5])
}

// upkeepAll is every item of a job, in the order they are offered.
func (s *Server) upkeepAll(ctx context.Context, job string) ([]map[string]any, error) {
	d := s.cfg.Upkeep.DB
	out := []map[string]any{}
	switch job {
	case "conflicts":
		skip, err := upkeep.Dismissed(ctx, d, "conflicts")
		if err != nil {
			return nil, err
		}
		pending, err := s.cfg.Memory.Pending(ctx, runCap)
		if err != nil {
			return nil, err
		}
		for _, c := range pending {
			if k := shortKey("c-", c.Path, c.Old, c.New); !skip[k] {
				out = append(out, map[string]any{"key": k, "path": c.Path, "old": c.Old, "new": c.New, "day": c.Day})
			}
		}
	case "duplicates":
		skip, err := upkeep.Dismissed(ctx, d, "duplicates")
		if err != nil {
			return nil, err
		}
		dups, err := s.cfg.Memory.DuplicateCandidates(ctx, s.areas, skip, 0)
		if err != nil {
			return nil, err
		}
		for _, p := range dups {
			out = append(out, map[string]any{"key": p.Key, "a": p.A, "b": p.B, "reason": p.Reason, "weight": p.Weight})
		}
	case "hubs":
		h, err := s.cfg.Memory.Health(ctx, s.areas, func(area string) int {
			if s.isLog(area) || area == "views" {
				return int(^uint(0) >> 1)
			}
			return hubWords
		}, nil)
		if err != nil {
			return nil, err
		}
		slices.SortStableFunc(h.TooLong, func(x, y memory.LongPage) int { return y.Words - x.Words })
		for _, p := range h.TooLong {
			// an episode or a source is a record, not a page the upkeep may rewrite
			if p.Kind == memory.KindEpisode || p.Kind == memory.KindSource || p.Kind == memory.KindTask {
				continue
			}
			out = append(out, map[string]any{"key": p.Page, "path": p.Page, "words": p.Words})
		}
	case "logs":
		logs, err := s.cfg.Tasks.LongLogs(ctx, runCap)
		if err != nil {
			return nil, err
		}
		for _, l := range logs {
			out = append(out, map[string]any{"key": l.ID, "id": l.ID, "path": l.Path, "words": l.Words})
		}
	case "weeks":
		days, err := s.diaryDays(ctx, true)
		if err != nil {
			return nil, err
		}
		today := time.Now()
		if !s.cfg.Now.IsZero() {
			today = s.cfg.Now
		}
		for _, w := range upkeep.DueWeeks(days, today.In(s.zone), 0) {
			paths := []string{}
			for _, d := range w.Days {
				paths = append(paths, d.Path)
			}
			out = append(out, map[string]any{"key": w.Key, "page": s.weekPage(w.Key), "ends": w.End, "days": paths})
		}
	case "inbox":
		entries, _, err := s.inboxEntries(ctx)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			// a cloud client's entry is the owner's to place: a model running the upkeep would launder into pages what
			// a model in someone else's cloud was told by a page or a mail
			if !e.cloud {
				out = append(out, map[string]any{"key": e.key, "text": e.text})
			}
		}
	default:
		return nil, refusal("job", "unknown", "use one of: "+strings.Join(jobs, ", "))
	}
	return out, nil
}

// weekPage is the page a week's summary lives on: diary/weeks/2026-w42.md.
func (s *Server) weekPage(key string) string {
	return slugPath(s.areas.Diary + "/" + s.weeksDir() + "/" + key)
}

// diaryDays is every diary day (not the week pages) as DueWeeks wants it; byPage also counts a day as summarised when
// its week page is a summary written after the day last changed.
func (s *Server) diaryDays(ctx context.Context, byPage bool) ([]upkeep.Day, error) {
	pages, err := s.cfg.Memory.List(ctx, s.areas.Diary+"/", memoryListAll)
	if err != nil {
		return nil, err
	}
	byPath := map[string]memory.Info{}
	for _, p := range pages {
		byPath[p.Path] = p
	}
	// a week page with at least three list items (as markWeek counts) is a summary: it stands for the days of its week
	// that were not changed after it, whether or not the days carry `summarised:`
	folded := map[string]bool{}
	var days []upkeep.Day
	for _, p := range pages {
		day := strings.TrimSuffix(strings.TrimPrefix(p.Path, s.areas.Diary+"/"), ".md")
		if !diaryDay.MatchString(day) {
			continue
		}
		doc, err := s.cfg.Memory.Get(ctx, p.Path)
		if err != nil {
			return nil, err
		}
		if doc == nil {
			continue
		}
		sum := memory.FrontMatter(doc.Body)["summarised"] != ""
		if key, _, err := upkeep.ISOWeek(day); byPage && err == nil && !sum {
			wp := s.weekPage(key)
			if _, seen := folded[wp]; !seen {
				w, err := s.cfg.Memory.Get(ctx, wp)
				if err != nil {
					return nil, err
				}
				folded[wp] = w != nil && len(listItem.FindAllString(withoutFront(w.Body), -1)) >= 3
			}
			if wi, ok := byPath[wp]; ok && folded[wp] && wi.Updated >= p.Updated {
				sum = true
			}
		}
		days = append(days, upkeep.Day{Path: p.Path, Day: day,
			HasLines: listItem.MatchString(withoutFront(doc.Body)), Summarised: sum})
	}
	return days, nil
}

// markWeek sets `summarised: <week page>` on the days of the week a page just written stands for, once it holds at
// least three list items (a title and nothing else is not a summary). Off unless Upkeep.MarkWeeks; a failure leaves
// the days unmarked, so they are offered again.
func (s *Server) markWeek(ctx context.Context, path, body string) {
	if s.cfg.Upkeep == nil || !s.cfg.Upkeep.MarkWeeks || !s.isWeekPath(path) || len(listItem.FindAllString(withoutFront(body), -1)) < 3 {
		return
	}
	days, err := s.diaryDays(ctx, false)
	if err != nil {
		return
	}
	for _, d := range days {
		key, _, err := upkeep.ISOWeek(d.Day)
		if err != nil || d.Summarised || !d.HasLines || s.weekPage(key) != path {
			continue
		}
		cur, err := s.cfg.Memory.Get(ctx, d.Path)
		if err != nil || cur == nil {
			continue
		}
		next := memory.SetFrontMatter(cur.Body, memory.FMValue{Key: "summarised", Value: strings.TrimSuffix(path, ".md")})
		_, _ = s.cfg.Memory.WriteAt(ctx, d.Path, next, s.by(ctx), cur.Rev)
	}
}

// upkeepState is what session_start tells of the last upkeep run: when it ended (started, while open), how it ended
// and its job; null when there has been none or the upkeep is not set up.
func (s *Server) upkeepState(ctx context.Context) (any, error) {
	if s.cfg.Upkeep == nil || s.cfg.Upkeep.DB == nil {
		return nil, nil
	}
	r, err := upkeep.Last(ctx, s.cfg.Upkeep.DB)
	if err != nil || r == nil {
		return nil, err
	}
	last, status := r.Ended, r.Status
	if last == "" {
		last, status = r.Started, "open"
	}
	return map[string]any{"last": last, "status": status, "job": r.Job}, nil
}

// survives refuses a change by the upkeep that takes a sentence out of a page without it being on the page still or on
// one of the pages in keptIn (read as they are now, so the destination is written first). The owner's writes are never
// checked. The inbox and the diary's days are left to the log rules (they are only added to, and a rewrite of a day may
// not drop a line), and a task's page never reaches here (the tasks tools own it); a task's log is checked like a page,
// which is how a long one is summed up: the summary goes above the entries, which stay.
func (s *Server) survives(ctx context.Context, path, before, after string, keptIn []string) error {
	if s.by(ctx) != upkeepAuthor {
		return nil
	}
	if s.isLogDay(path) {
		return nil
	}
	kept, err := s.keptBodies(ctx, keptIn, path)
	if err != nil {
		return err
	}
	lost := upkeep.Lost(before, after, kept)
	if len(lost) == 0 {
		return nil
	}
	return refusal("body", fmt.Sprintf("%d sentence(s) would be lost: %s", len(lost), quoteLost(lost)),
		"write the page they go to first, then name it in kept_in; or keep them in this page")
}

// isLogDay is the inbox page or a diary day: the pages that are only added to. Week pages are ordinary pages.
func (s *Server) isLogDay(path string) bool {
	area := areaOf(path)
	return area == s.areas.Inbox || (area == s.areas.Diary && !s.isWeekPath(path))
}

// --- the inbox ---

type inboxEntry struct {
	key, text string
	at        []int    // the line of the page of each of its lines (a blank line may sit inside an entry)
	lines     []string // the entry as written, from its «- » line to the last continuation
	check     string   // what must be kept: its sentences, without the date head and the why
	cloud     bool     // a cloud client left it (its why line is cloudWhy's): it waits for the owner, not the upkeep
}

// inboxEntries reads the inbox page: each entry is a «- » line and the indented lines under it.
func (s *Server) inboxEntries(ctx context.Context) ([]inboxEntry, *memory.Doc, error) {
	doc, err := s.cfg.Memory.Get(ctx, s.areas.Inbox+"/inbox.md")
	if err != nil || doc == nil {
		return nil, doc, err
	}
	var out []inboxEntry
	for n, l := range strings.Split(doc.Body, "\n") {
		switch {
		case strings.HasPrefix(l, "- "):
			out = append(out, inboxEntry{at: []int{n}, lines: []string{l}})
		case len(out) > 0 && strings.HasPrefix(l, " ") && strings.TrimSpace(l) != "":
			e := &out[len(out)-1]
			e.lines, e.at = append(e.lines, l), append(e.at, n)
		}
	}
	for i := range out {
		e := &out[i]
		e.key = shortKey("i-", e.lines...)
		var text []string
		for j, l := range e.lines {
			if j == 0 {
				l = inboxEntryHead.ReplaceAllString(l, "")
			}
			l = strings.TrimSpace(l)
			if m := whyLine.FindStringSubmatch(l); j > 0 && m != nil {
				e.cloud = e.cloud || isCloudWhy(m[1])
			}
			if !strings.HasPrefix(l, "why:") {
				text = append(text, l)
			}
		}
		e.check = strings.Join(text, "\n")
		e.text = strings.Join(text, " ")
	}
	return out, doc, nil
}

// inboxPlaced takes the named entries out of the inbox, only when every sentence of each is on the pages in kept_in as
// they are now: the entry is the one copy of what was said until then.
func (s *Server) inboxPlaced(ctx context.Context, a protocol.MemoryUpkeep) (any, error) {
	if len(a.Items) == 0 || len(a.KeptIn) == 0 {
		return nil, needs("op inbox_placed", field{"items", len(a.Items) == 0}, field{"kept_in", len(a.KeptIn) == 0})
	}
	kept, err := s.keptBodies(ctx, a.KeptIn, s.areas.Inbox+"/inbox.md")
	if err != nil {
		return nil, err
	}
	entries, doc, err := s.inboxEntries(ctx)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, k := range a.Items {
		want[k] = true
	}
	drop := map[string]bool{}
	var lost []string
	for _, e := range entries {
		if !want[e.key] {
			continue
		}
		delete(want, e.key)
		if e.cloud && s.by(ctx) == upkeepAuthor {
			return nil, refusal("items", e.key+" came from a cloud client: it waits for the owner", "leave it in the inbox and take it out of items")
		}
		// every sentence and fragment of the entry, short ones too: «Her pin: 4821.» is a fact
		if l := upkeep.Missing(e.check, kept); len(l) > 0 {
			lost = append(lost, l...)
			continue
		}
		drop[e.key] = true
	}
	var issues []issue
	for k := range want {
		issues = append(issues, issue{"items", k + " is not in the inbox", "read the worklist again: the entry may be placed already"})
	}
	if len(lost) > 0 {
		issues = append(issues, issue{"kept_in", "not on those pages: " + quoteLost(lost),
			"write these sentences to the pages first, then call inbox_placed again"})
	}
	if len(issues) > 0 {
		return nil, &callError{issues}
	}
	gone := map[int]bool{}
	for _, e := range entries {
		if drop[e.key] {
			for _, n := range e.at {
				gone[n] = true
			}
		}
	}
	var keep []string
	for i, l := range strings.Split(doc.Body, "\n") {
		if !gone[i] {
			keep = append(keep, l)
		}
	}
	if _, err := s.cfg.Memory.WriteAt(ctx, doc.Path, strings.Join(keep, "\n"), s.by(ctx), doc.Rev); err != nil {
		return nil, err
	}
	return map[string]any{"removed": len(drop)}, nil
}

// keptBodies reads the pages a text was moved to. self is the page being changed: it cannot vouch for itself (the check
// would pass whatever was cut), and the inbox and the diary's days are no destination either, for they are only
// added to and may be emptied or rewritten after.
func (s *Server) keptBodies(ctx context.Context, paths []string, self string) ([]string, error) {
	var out []string
	for _, p := range paths {
		d, err := s.cfg.Memory.Get(ctx, slugPath(p))
		if err != nil {
			return nil, err
		}
		if d == nil {
			return nil, refusal("kept_in", p+" does not exist", "write the destination page first, then name it")
		}
		if d.Path == self || slugPath(p) == slugPath(self) {
			return nil, refusal("kept_in", p+" is the page being changed", "name the page the text goes to, not this one")
		}
		if s.isLogDay(d.Path) {
			return nil, refusal("kept_in", p+" is the inbox or a diary day: it is only added to", "write the text to a page of the memory, and name that")
		}
		out = append(out, d.Body)
	}
	return out, nil
}

// quoteLost lists up to five lost sentences, each cut short.
func quoteLost(lost []string) string {
	var q []string
	for i, l := range lost {
		if i == 5 {
			q = append(q, fmt.Sprintf("and %d more", len(lost)-5))
			break
		}
		r := []rune(strings.Join(strings.Fields(l), " "))
		if len(r) > 160 {
			r = append(r[:160], '…')
		}
		q = append(q, fmt.Sprintf("%q", string(r)))
	}
	return strings.Join(q, "; ")
}
