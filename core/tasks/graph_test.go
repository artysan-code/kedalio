package tasks

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"kedalio/core/memory"
)

func query(t *testing.T, b *Book, f Filter) []string {
	t.Helper()
	f.Me = "alice"
	return must(Query(ctx, b.DB, f))
}

func sorted(xs []string) []string {
	out := append([]string{}, xs...)
	sort.Strings(out)
	return out
}

func TestStatusesBelongToGroups(t *testing.T) {
	var got []Group
	for _, s := range Statuses {
		got = append(got, GroupOf(s))
	}
	if !reflect.DeepEqual(got, []Group{GroupOpen, GroupActive, GroupPaused, GroupClosed, GroupClosed}) {
		t.Error(got)
	}
}

func TestTheIndexFollowsEveryWriteAndAnswersTheFiltersBlocksIsDerived(t *testing.T) {
	b := newBook(t)
	parent := mustAdd(t, b, Input{Title: "Release"})
	a := mustAdd(t, b, Input{Title: "Tests", PartOf: L(parent.ID, "work/acme"), About: L("kedalio"), For: S("bob")})
	c := mustAdd(t, b, Input{Title: "Deploy", BlockedBy: L(a.ID), WorkedBy: S("run:r-7")})
	d := mustAdd(t, b, Input{Title: "Reply", AssignedTo: S("Cora Rossi"), WaitingOn: S("2026-11-01"), Due: S("2026-10-30")})

	only := func(id string) []string { return []string{id} }
	check := func(name string, got, want []string) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
	check("parent", query(t, b, Filter{Parent: parent.ID}), only(a.ID))
	check("project", query(t, b, Filter{Project: "work"}), only(a.ID))
	check("about", query(t, b, Filter{About: "Kedalio"}), only(a.ID))
	check("for", query(t, b, Filter{For: "bob"}), only(a.ID))
	// no `for` means the account
	check("for alice", sorted(query(t, b, Filter{For: "alice"})), sorted([]string{parent.ID, c.ID, d.ID}))
	check("worked_by", query(t, b, Filter{WorkedBy: "run:r-7"}), only(c.ID))
	check("worked_by any", query(t, b, Filter{WorkedBy: "any"}), only(c.ID))
	check("assigned_to", query(t, b, Filter{AssignedTo: "cora-rossi"}), only(d.ID))
	check("waiting_on", query(t, b, Filter{WaitingOn: "2026-11-01"}), only(d.ID))
	check("blocked_by", query(t, b, Filter{BlockedBy: a.ID}), only(c.ID))
	check("blocks", query(t, b, Filter{Blocks: c.ID}), only(a.ID))
	check("BlocksOf", must(BlocksOf(ctx, b.DB, a.ID)), only(c.ID))
	if bl := must(BlockedIDs(ctx, b.DB)); len(bl) != 1 || !bl[c.ID] {
		t.Errorf("%v", bl)
	}
	// by day, undated last
	eq(t, query(t, b, Filter{Group: []Group{GroupOpen}})[0], d.ID)
	for _, id := range query(t, b, Filter{AssignedTo: "alice"}) {
		if id == d.ID {
			t.Error("cora's task is not alice's")
		}
	}

	mustUpdate(t, b, a.ID, Input{Status: Done})
	check("closed", query(t, b, Filter{Group: []Group{GroupClosed}}), only(a.ID))
	eq(t, len(must(BlockedIDs(ctx, b.DB))), 0) // a done blocker releases the task
	mustUpdate(t, b, c.ID, Input{WorkedBy: S(""), Status: Doing})
	check("active", query(t, b, Filter{Group: []Group{GroupActive}}), only(c.ID))
	eq(t, len(query(t, b, Filter{WorkedBy: "any"})), 0)

	// the index is derived: rebuilt from the pages it is the same
	dump := func() string {
		var sb strings.Builder
		for _, q := range []string{"select * from task_index order by id", "select * from task_rels order by id, rel, key"} {
			rows := must(b.DB.Query(q))
			cols := must(rows.Columns())
			for rows.Next() {
				vals := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				rows.Scan(ptrs...)
				sb.WriteString(strings.Join(strings.Fields(strings.Trim(fmt.Sprint(vals), "[]")), "|") + "\n")
			}
			rows.Close()
		}
		return sb.String()
	}
	before := dump()
	eq(t, must(b.Reindex(ctx)), 4)
	eq(t, dump(), before)
	// the task pages are of kind task; a removed task leaves the index
	eq(t, string(must(b.Store.Get(ctx, "tasks/"+a.ID+".md")).Kind), "task")
	if err := b.Store.Remove(ctx, "tasks/"+d.ID+".md", "test"); err != nil {
		t.Fatal(err)
	}
	for _, id := range query(t, b, Filter{}) {
		if id == d.ID {
			t.Error("removed")
		}
	}
}

func TestAPersonNamedInATaskPointsAtTheirPageANameNobodyAnswersToMakesNoPage(t *testing.T) {
	b := newBook(t)
	write(t, b, "persone/dana-bianchi.md", page("Dana Bianchi"))
	tk := mustAdd(t, b, Input{Title: "Chiedere", AssignedTo: S("Dana Bianchi"), For: S("Cora Rossi")})
	var got []string
	for _, r := range RelsOf(tk) {
		got = append(got, r.Rel+"="+string(r.Kind)+":"+r.Target)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"assigned_to=page:persone/dana-bianchi", "for=name:cora-rossi"}) {
		t.Error(got)
	}
	if d, _ := b.Store.Get(ctx, "persone/cora-rossi.md"); d != nil {
		t.Error("no stub page")
	}
	if !reflect.DeepEqual(UnresolvedPeople(tk), []string{"cora-rossi"}) {
		t.Error(UnresolvedPeople(tk))
	}
	// the person's page lists their tasks: the task is among its backlinks, by name of the relation
	links := must(b.Store.Links(ctx, "persone/dana-bianchi.md", nil))
	if !reflect.DeepEqual(links.Related, []memory.Related{{From: "tasks/" + tk.ID + ".md", Rel: "assigned_to"}}) {
		t.Errorf("%+v", links.Related)
	}
	// the plain name is in the file and in the index, and reads back
	eq(t, strings.Contains(must(b.Store.Get(ctx, "tasks/"+tk.ID+".md")).Body, "- for name:cora-rossi"), true)
	eq(t, len(query(t, b, Filter{For: "cora-rossi"})), 1)
	eq(t, ForOf(mustGet(t, b, tk.ID)).Kind, KindName)
	eq(t, len(must(b.Store.List(ctx, "persone/", memory.ListOpts{}))), 1)
}

func TestPeopleRolesAndCompaniesAreNeverPagesFirstNamePrefixAkaAndEntitiesResolve(t *testing.T) {
	b := newBook(t)
	write(t, b, "persone/anna-cruz.md", page("Anna Cruz"))
	write(t, b, "persone/viktor-ostrov.md", "---\naka: Vik, Acmeco\n---\n# Viktor Ostrov\n\nA person.\n")
	write(t, b, "persone/anna-neri.md", page("Anna Neri"))
	write(t, b, "persone/mina-lupo.md", page("Mina Lupo"))
	// an entity outside the people area that says what it is
	write(t, b, "clienti/acme.md", "---\nkind: entity\nis: organization\n---\n# Acme\n\nA client.\n")
	write(t, b, "notes/idea.md", "---\nkind: entity\nis: project\n---\n# Idea\n\nNot a person.\n")
	who := func(n string) string {
		tk := mustAdd(t, b, Input{Title: "x " + n, AssignedTo: S(n)})
		for _, r := range RelsOf(tk) {
			if r.Rel == "assigned_to" {
				return string(r.Kind) + ":" + r.Target
			}
		}
		return ""
	}
	for in, want := range map[string]string{
		"Mina Lupo":            "page:persone/mina-lupo", // exact slug
		"mina":                 "page:persone/mina-lupo", // the only mina-*
		"anna":                 "name:anna",              // two anna-*: not guessed
		"Anna Cruz":            "page:persone/anna-cruz",
		"Vik":                  "page:persone/viktor-ostrov", // aka
		"acmeco":               "page:persone/viktor-ostrov", // aka, any case
		"viktor":               "page:persone/viktor-ostrov", // first name
		"Acme":                 "page:clienti/acme",          // an organization outside the people area
		"idea":                 "name:idea",                  // a project is not a person
		"cliente (via giulia)": "name:cliente-via-giulia",
	} {
		if got := who(in); got != want {
			t.Errorf("%q: %s, want %s", in, got, want)
		}
	}
	for _, role := range []string{"lead", "cto", "deepagent"} {
		if !strings.HasPrefix(who(role), "name:") {
			t.Error(role)
		}
	}
	eq(t, len(must(b.Store.List(ctx, "persone/", memory.ListOpts{}))), 4) // no page was made
	// waiting_on too, and the index finds the plain name
	w := mustAdd(t, b, Input{Title: "attesa", Status: Waiting, WaitingOn: S("cto")})
	eq(t, strings.Join(query(t, b, Filter{WaitingOn: "cto"}), ","), w.ID)
	// a page made later is found on the next write of the task
	again := mustUpdate(t, b, w.ID, Input{Title: "attesa 2"}).Task
	eq(t, strings.Join(UnresolvedPeople(again), ","), "cto")
	write(t, b, "persone/cto.md", page("Cto Person"))
	eq(t, RelsOf(mustUpdate(t, b, w.ID, Input{Title: "attesa 3"}).Task)[0].Target, "persone/cto")
}
func TestBriefYoursWaitingOnOthersInProgressByAgentsAndTasksForAnotherPersonApart(t *testing.T) {
	mk := func(id string, rels []Rel, over func(*Task)) Task {
		tk := task(func(t *Task) { t.ID, t.Title, t.Rels = id, id, rels })
		if over != nil {
			over(&tk)
		}
		return tk
	}
	pageRel := func(rel, target string) Rel { return Rel{Rel: rel, Kind: KindPage, Target: target} }
	now := at("2026-10-26T10:00:00")
	b := Brief([]Task{
		mk("mine", nil, nil),
		mk("mine-by-claude", []Rel{{Rel: "worked_by", Kind: KindEngine, Target: "claude"}}, nil),
		mk("for-me", []Rel{pageRel("for", "persone/alice")}, nil),
		mk("for-bob", []Rel{pageRel("for", "persone/bob")}, nil),
		mk("to-cora", []Rel{pageRel("assigned_to", "persone/cora")}, nil),
		mk("to-me", []Rel{pageRel("assigned_to", "alice")}, nil),
		mk("on-date", []Rel{{Rel: "waiting_on", Kind: KindDate, Target: "2026-11-01"}}, nil),
		mk("paused", nil, func(t *Task) { t.Status = Waiting }),
		mk("by-run", []Rel{{Rel: "worked_by", Kind: KindRun, Target: "r-1"}}, func(t *Task) { t.Status = Doing }),
		mk("bob-by-run", []Rel{pageRel("for", "bob"), {Rel: "worked_by", Kind: KindRun, Target: "r-2"}}, nil),
		mk("closed-for-bob", []Rel{pageRel("for", "bob")}, func(t *Task) { t.Status = Done }),
	}, now, "alice", time.UTC)
	eq(t, strings.Join(sorted(idsList(b.Undated)), ","), "for-me,mine,mine-by-claude,to-me")
	eq(t, strings.Join(sorted(idsList(b.Waiting)), ","), "on-date,paused,to-cora")
	eq(t, strings.Join(idsList(b.Agents), ","), "by-run")
	eq(t, strings.Join(sorted(idsList(b.ForOthers)), ","), "bob-by-run,for-bob")
}

func TestBriefOverdueMissedTheRestOfTodayByTimeTomorrowWaitingOnOthers(t *testing.T) {
	now := at("2026-09-30T11:00:00")
	mk := func(id, title string, over func(*Task)) Task {
		return task(func(t *Task) {
			t.ID, t.Title = id, title
			if over != nil {
				over(t)
			}
		})
	}
	b := Brief([]Task{
		mk("a", "old", func(t *Task) { t.Due = "2026-09-28" }),
		mk("b", "at 9", func(t *Task) { t.Due, t.Time = "2026-09-30", "09:00" }),
		mk("c", "at 15", func(t *Task) { t.Due, t.Time = "2026-09-30", "15:00" }),
		mk("d", "sometime today", func(t *Task) { t.Due = "2026-09-30" }),
		mk("e", "tomorrow", func(t *Task) { t.Due = "2026-10-01" }),
		mk("f", "in two days", func(t *Task) { t.Due = "2026-10-02" }),
		mk("g", "Leo sends the quote", func(t *Task) { t.Rels = []Rel{{Rel: "assigned_to", Kind: KindPage, Target: "leo"}} }),
		mk("h", "done", func(t *Task) { t.Status, t.Done = Done, "2026-09-30T09:30:00.000Z" }),
	}, now, "alice", time.UTC)
	for name, got := range map[string][]Task{"overdue": b.Overdue, "missed": b.Missed, "today": b.Today,
		"tomorrow": b.Tomorrow, "upcoming": b.Upcoming, "waiting": b.Waiting} {
		want := map[string]string{"overdue": "a", "missed": "b", "today": "c,d", "tomorrow": "e", "upcoming": "f", "waiting": "g"}[name]
		if strings.Join(idsList(got), ",") != want {
			t.Errorf("%s: %v, want %s", name, idsList(got), want)
		}
	}
	eq(t, b.DoneToday, 1)
	eq(t, b.Moment, Moment("morning"))
}
