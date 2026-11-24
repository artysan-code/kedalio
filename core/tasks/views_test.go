package tasks

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"kedalio/core/memory"
)

func viewPageOf(front ...string) string {
	return "---\n" + strings.Join(front, "\n") + "\n---\n\n# V\n\nA view.\n"
}

func TestParseViewTheFormatWithDefaults(t *testing.T) {
	v, err := ParseView("week", "---\nstatus: todo, doing\nto: today+7\nfor: bob\ngroup_by: due-week\nsort: -priority\nlayout: board\nlimit: 20\n---\n\n# This week\n\nx\n")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, v.Title, "This week")
	if !reflect.DeepEqual(v.Filter.Status, []Status{Todo, Doing}) || v.Filter.To != "today+7" || v.Filter.For != "bob" {
		t.Errorf("%+v", v.Filter)
	}
	eq(t, v.Layout+"|"+v.GroupBy+"|"+v.Sort.By+"|"+string(rune('0'+v.Limit/10))+string(rune('0'+v.Limit%10)), "board|due-week|priority|20")
	eq(t, v.Sort.Desc, true)
	bare := must(ParseView("x", viewPageOf("kind: concept")))
	eq(t, bare.Layout, "list")
	eq(t, bare.GroupBy, "")
	eq(t, bare.Sort, SortBy{"due", false})
	eq(t, bare.Title, "V")
	all := must(ParseView("all", viewPageOf("status: all", "stalled: false")))
	eq(t, all.Filter.AllStatuses, true)
	eq(t, *all.Filter.Stalled, false)
}

func TestParseViewAWrongLineIsNamedInTheError(t *testing.T) {
	for _, c := range [][2]string{
		{"colour: red", `unknown line "colour"`},
		{"status: started", "status is all or some of"},
		{"group: busy", "group is some of"},
		{"status: todo\ngroup: open", "status and group together"},
		{"layout: grid", "layout is list or board"},
		{"group_by: mood", "group_by is one of project, status, about, due-week, for, day"},
		{"sort: size", "sort is one of"},
		{"limit: 0", "limit is a number from 1 to 500"},
		{"stalled: maybe", "stalled is true or false"},
		{"to: tomorrow", "YYYY-MM-DD, today, today+N or today-N"},
	} {
		_, err := ParseView("v", viewPageOf(strings.Split(c[0], "\n")...))
		var ve *ViewError
		if err == nil || !strings.Contains(err.Error(), c[1]) || !strings.HasPrefix(err.Error(), "view v: ") {
			t.Errorf("%q: %v", c[0], err)
		}
		_ = ve
	}
}

func TestResolveDayAndWeekOf(t *testing.T) {
	for _, c := range [][2]string{{"today", "2026-10-26"}, {"today+7", "2026-11-02"}, {"today-30", "2026-09-26"}, {"2026-12-01", "2026-12-01"}} {
		eq(t, must(ResolveDay(c[0], "2026-10-26", "a day")), c[1])
	}
	for day, want := range map[string]string{"2026-10-26": "2026-W44", "2026-11-01": "2026-W44", "2027-01-01": "2026-W53", "2024-12-30": "2025-W01"} {
		eq(t, WeekOf(day), want)
	}
}

func viewNames(vs []ListedView) []string {
	out := []string{}
	for _, v := range vs {
		out = append(out, v.Name)
	}
	return out
}

func TestTheShippedViewsExistAreValidADeletedOneStaysDeleted(t *testing.T) {
	b := newBook(t)
	if err := b.EnsureViews(ctx); err != nil {
		t.Fatal(err)
	}
	got := must(b.ListViews(ctx))
	for _, v := range got {
		eq(t, v.Error, "")
	}
	eq(t, strings.Join(viewNames(got), ","), "board,days,per-altri,stalled,today")
	eq(t, got[0].Layout, "board")
	// the owner's own version of a view is kept; a deleted one stays deleted
	write(t, b, "views/today.md", "---\nstatus: todo\n---\n\n# Mine\n\nx\n")
	if err := b.Store.Remove(ctx, "views/board.md", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := b.EnsureViews(ctx); err != nil {
		t.Fatal(err)
	}
	eq(t, must(b.ReadView(ctx, "today")).Title, "Mine")
	_, err := b.ReadView(ctx, "board")
	wantErr(t, err, `no view "board"`)
	eq(t, strings.Join(viewNames(must(b.ListViews(ctx))), ","), "days,per-altri,stalled,today")
	var ops []string
	for _, h := range must(b.Store.History(ctx, "views/board.md")) {
		ops = append([]string{string(h.Op)}, ops...)
	}
	eq(t, strings.Join(ops, ","), "write,delete")
	eq(t, string(must(b.Store.Get(ctx, "views/stalled.md")).Kind), "concept")
}

func TestReadViewANameThatIsNoViewListsTheOnesThereAre(t *testing.T) {
	b := newBook(t)
	must(0, b.EnsureViews(ctx))
	_, err := b.ReadView(ctx, "nope")
	wantErr(t, err, "there are: board, days, per-altri, stalled, today")
	_, err = b.ReadView(ctx, "../x")
	wantErr(t, err, `no view "../x"`)
	write(t, b, "views/broken.md", viewPageOf("layout: grid"))
	_, err = b.ReadView(ctx, "views/broken")
	wantErr(t, err, "layout is list or board")
	for _, v := range must(b.ListViews(ctx)) {
		if v.Name == "broken" {
			eq(t, v.Error, "view broken: layout is list or board")
		}
	}
}

type viewFixture struct {
	b                *Book
	a, t, n, u, done Task
}

func fixture(t *testing.T) viewFixture {
	b := newBook(t)
	must(0, b.EnsureViews(ctx))
	write(t, b, "persone/bob.md", page("Bob"))
	write(t, b, "progetti/kedalio.md", page("Kedalio"))
	b.clockAt("2026-10-20T10:00:00")
	f := viewFixture{b: b}
	f.a = mustAdd(t, b, Input{Title: "Late one", Due: S("2026-10-20"), Priority: N(3), Project: S("work/acme")})
	f.t = mustAdd(t, b, Input{Title: "Due today", Due: S("2026-10-26"), Priority: N(1), Project: S("work/acme"), About: L("progetti/kedalio")})
	f.n = mustAdd(t, b, Input{Title: "Next week", Due: S("2026-11-03"), For: S("persone/bob"), About: L("progetti/kedalio", "persone/bob")})
	f.u = mustAdd(t, b, Input{Title: "No date", Status: Doing, Project: S("personal/blog")})
	f.done = mustAdd(t, b, Input{Title: "Finished", Due: S("2026-10-26")})
	mustUpdate(t, b, f.done.ID, Input{Status: Done})
	b.clockAt("2026-10-26T10:00:00")
	return f
}

// groupsOf is a run view as group key and task titles.
func groupsOf(r ViewResult) [][2]any {
	var out [][2]any
	for _, g := range r.Group {
		out = append(out, [2]any{g.Key, strings.Join(titles(g.Tasks), "|")})
	}
	return out
}

func runPage(t *testing.T, f viewFixture, front ...string) ViewResult {
	t.Helper()
	v, err := ParseView("v", viewPageOf(front...))
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.b.RunView(ctx, v, at("2026-10-26T10:00:00"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunViewTodayBoardAndStalledAsTheyShip(t *testing.T) {
	f := fixture(t)
	now := at("2026-10-26T10:00:00")
	today := must(f.b.RunView(ctx, must(f.b.ReadView(ctx, "today")), now))
	eq(t, len(today.Group), 1)
	eq(t, strings.Join(titles(today.Group[0].Tasks), "|"), "Late one|Due today")
	board := must(f.b.RunView(ctx, must(f.b.ReadView(ctx, "board")), now))
	var keys []string
	for _, g := range board.Group {
		keys = append(keys, g.Key)
	}
	eq(t, strings.Join(keys, ","), "todo,doing,waiting")
	eq(t, strings.Join(titles(board.Group[0].Tasks), "|"), "Due today|Next week|Late one")
	eq(t, len(board.Group[2].Tasks), 0)
	// nothing is more than a fortnight late; the one doing has a recent write
	eq(t, must(f.b.RunView(ctx, must(f.b.ReadView(ctx, "stalled")), now)).Count, 0)
}

func TestRunViewTheFiltersOfTheGraphTheGroupsAndTheSorts(t *testing.T) {
	f := fixture(t)
	one := func(want string, front ...string) {
		t.Helper()
		r := runPage(t, f, front...)
		eq(t, len(r.Group), 1)
		eq(t, strings.Join(titles(r.Group[0].Tasks), "|"), want)
	}
	one("Next week|Due today", "about: kedalio", "sort: -due")
	one("Next week", "for: bob")
	one("No date", "assigned_to: alice", "status: doing")
	one("Finished", "status: done")
	var keys []string
	for _, g := range runPage(t, f, "status: all", "group_by: status").Group {
		keys = append(keys, g.Key)
	}
	eq(t, strings.Join(keys, ","), "todo,doing,done")
	if got := groupsOf(runPage(t, f, "group_by: project", "sort: title")); !reflect.DeepEqual(got, [][2]any{
		{"personal/blog", "No date"}, {"work/acme", "Due today|Late one"}, {"", "Next week"}}) {
		t.Errorf("%v", got)
	}
	// a task about two pages is in both groups
	var counts []string
	for _, g := range runPage(t, f, "group_by: about").Group {
		counts = append(counts, g.Key+":"+string(rune('0'+len(g.Tasks))))
	}
	eq(t, strings.Join(counts, ","), "bob:1,kedalio:2,:2")
	if got := groupsOf(runPage(t, f, "group_by: due-week", "from: 2026-10-20", "to: today+10")); !reflect.DeepEqual(got, [][2]any{
		{"2026-W43", "Late one"}, {"2026-W44", "Due today"}, {"2026-W45", "Next week"}}) {
		t.Errorf("%v", got)
	}
	keys = nil
	for _, g := range runPage(t, f, "group_by: for").Group {
		keys = append(keys, g.Key)
	}
	eq(t, strings.Join(keys, ","), "alice,bob")
	// by day: overdue, today, tomorrow, later, undated — in that order, empty buckets left out, keys as labels
	days := runPage(t, f, "group_by: day")
	if got := groupsOf(days); !reflect.DeepEqual(got, [][2]any{
		{"overdue", "Late one"}, {"today", "Due today"}, {"later", "Next week"}, {"undated", "No date"}}) {
		t.Errorf("%v", got)
	}
	var labels []string
	for _, g := range days.Group {
		labels = append(labels, g.Label)
	}
	eq(t, strings.Join(labels, ","), "overdue,today,later,undated")
	for day, want := range map[string]string{"2026-10-25": "overdue", "2026-10-26": "today", "2026-10-27": "tomorrow", "2026-10-28": "later", "": "undated"} {
		eq(t, DayBucket(day, "2026-10-26"), want)
	}
	one("Due today|Next week", "sort: priority", "limit: 2")
	eq(t, runPage(t, f, "group: active").Count, 1)
	_, err := f.b.RunView(ctx, must(ParseView("v", viewPageOf("parent: t-nonexistent-1"))), at("2026-10-26T10:00:00"))
	wantErr(t, err, "view v:")
	eq(t, GroupKeys(f.t, "for", "alice", "", "")[0], "alice")
}

func TestRunViewStalledKeepsTheFindingsWithTheirReasonsOrLeavesThemOut(t *testing.T) {
	f := fixture(t)
	later := at("2026-12-01T10:00:00")
	run := func(front string, at time.Time) ViewResult {
		return must(f.b.RunView(ctx, must(ParseView("v", viewPageOf(front))), at))
	}
	r := run("stalled: true", later)
	eq(t, r.Reasons[f.a.ID][0].Code, Overdue)
	eq(t, r.Reasons[f.u.ID][0].Code, DoingIdle)
	eq(t, run("stalled: false", later).Count, 0)
	eq(t, run("stalled: false", at("2026-10-26T10:00:00")).Count, 4)
}

func TestPerAltriWhatWaitsOnSomeoneIsAssignedElsewhereOrIsForSomeoneByPersonNotTheOwners(t *testing.T) {
	b := newBook(t)
	must(0, b.EnsureViews(ctx))
	for _, p := range []string{"persone/bob", "persone/cora", "io/chi-sono"} {
		write(t, b, p+".md", page(p))
	}
	mustAdd(t, b, Input{Title: "Answer from Bob", Status: Waiting, WaitingOn: S("persone/bob")})
	mustAdd(t, b, Input{Title: "Cora does it", AssignedTo: S("persone/cora")})
	mustAdd(t, b, Input{Title: "A gift for Bob", For: S("persone/bob")})
	mustAdd(t, b, Input{Title: "The owner tests it", Status: Waiting, WaitingOn: S("io/chi-sono")})
	mustAdd(t, b, Input{Title: "Mine"})
	r := must(b.RunView(ctx, must(b.ReadView(ctx, "per-altri")), at("2026-10-26T10:00:00")))
	by := map[string]string{}
	var keys []string
	for _, g := range r.Group {
		keys = append(keys, g.Key)
		ts := titles(g.Tasks)
		by[g.Key] = strings.Join(sorted(ts), "|")
	}
	eq(t, strings.Join(sorted(keys), ","), "bob,cora")
	eq(t, by["bob"], "A gift for Bob|Answer from Bob")
	eq(t, by["cora"], "Cora does it")
}

func TestTheOthersViewIsNamedInTheAccountsLanguageAndAnExistingNameIsKept(t *testing.T) {
	b := newBook(t)
	b.Store.Areas = memory.AreasEN
	if err := b.EnsureViews(ctx); err != nil {
		t.Fatal(err)
	}
	eq(t, strings.Join(viewNames(must(b.ListViews(ctx))), ","), "board,days,others,stalled,today")

	// a database that already has the Italian name is not given the English one
	b = newBook(t)
	write(t, b, "views/per-altri.md", "---\nothers: true\n---\n\n# Mine\n\nx\n")
	b.Store.Areas = memory.AreasEN
	if err := b.EnsureViews(ctx); err != nil {
		t.Fatal(err)
	}
	eq(t, strings.Join(viewNames(must(b.ListViews(ctx))), ","), "board,days,per-altri,stalled,today")
}
