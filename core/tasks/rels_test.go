package tasks

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestClassifyWhatAWrittenTargetIs(t *testing.T) {
	for in, want := range map[string]Rel{
		"run:r-12":                {Kind: KindRun, Target: "r-12"},
		"engine:Claude":           {Kind: KindEngine, Target: "Claude"},
		"2026-11-10":              {Kind: KindDate, Target: "2026-11-10"},
		"[[t-20261026-aaaaaa]]":   {Kind: KindTask, Target: "t-20261026-aaaaaa"},
		"tasks/t-20261026-aaaaaa": {Kind: KindTask, Target: "t-20261026-aaaaaa"},
		"persone/alice.md":        {Kind: KindPage, Target: "persone/alice"},
	} {
		k, target := classify(in)
		if k != want.Kind || target != want.Target {
			t.Errorf("%q -> %s %q", in, k, target)
		}
	}
}

func TestTheRelationsSectionPagesAndTasksAreLinksRunsAndDatesPlainItRoundTrips(t *testing.T) {
	rels := []Rel{
		{Rel: "about", Kind: KindPage, Target: "kedalio"},
		{Rel: "worked_by", Kind: KindRun, Target: "r-12"},
		{Rel: "waiting_on", Kind: KindDate, Target: "2026-11-10"},
		{Rel: "part_of", Kind: KindTask, Target: "t-1", Note: "phase 2"},
		{Rel: "plays_with", Kind: KindPage, Target: "bob"},
	}
	text := sectionOf(rels, []string{"- not an edge"})
	eq(t, text, "## Relations\n\n- about [[kedalio]]\n- part_of [[t-1]] (phase 2)\n- worked_by run:r-12\n- waiting_on 2026-11-10\n- plays_with [[bob]]\n- not an edge")
	back, extra, rest := takeSection("Descrizione.\n\n" + text + "\n\n## Steps\n\n- [ ] uno\n")
	eq(t, rest, "Descrizione.\n\n## Steps\n\n- [ ] uno")
	if !reflect.DeepEqual(extra, []string{"- not an edge"}) || len(back) != 5 {
		t.Errorf("%v %v", extra, back)
	}
	for _, r := range back {
		if r.Rel == "part_of" && r != rels[3] {
			t.Errorf("%+v", r)
		}
	}
	if r, _, _ := takeSection("niente"); len(r) != 0 {
		t.Error("no section")
	}
}

func TestEditRelsPersonsOnlyForAssignedToAndForTheAccountIsTheDefault(t *testing.T) {
	edit := func(base []Rel, in Input) ([]Rel, error) { return EditRels(base, in, "alice", "") }
	_, err := edit(nil, Input{AssignedTo: S("engine:claude")})
	wantErr(t, err, "worked_by")
	_, err = edit(nil, Input{WorkedBy: S("[[bob]]")})
	wantErr(t, err, "agent")
	_, err = edit(nil, Input{BlockedBy: L("notatask")})
	wantErr(t, err, "task ids")
	got, _ := edit(nil, Input{AssignedTo: S("alice"), For: S("Alice")})
	eq(t, len(got), 0)
	got, _ = edit(nil, Input{For: S("bob")})
	if !reflect.DeepEqual(got, []Rel{{Rel: "for", Kind: KindPage, Target: "bob"}}) {
		t.Errorf("%+v", got)
	}
	got, _ = edit(nil, Input{WorkedBy: S("claude")})
	if !reflect.DeepEqual(got, []Rel{{Rel: "worked_by", Kind: KindEngine, Target: "claude"}}) {
		t.Errorf("%+v", got)
	}
	got, _ = edit(nil, Input{WorkedBy: S("scout")})
	if !reflect.DeepEqual(got, []Rel{{Rel: "worked_by", Kind: KindAgent, Target: "scout"}}) {
		t.Errorf("%+v", got)
	}
	got, _ = edit(nil, Input{WaitingOn: S("2026-12-01")})
	if !reflect.DeepEqual(got, []Rel{{Rel: "waiting_on", Kind: KindDate, Target: "2026-12-01"}}) {
		t.Errorf("%+v", got)
	}
	// the whole list is replaced; an empty one clears
	two, _ := edit(nil, Input{About: L("a", "b", "a")})
	targets := func(rs []Rel) []string {
		out := []string{}
		for _, r := range rs {
			out = append(out, r.Target)
		}
		return out
	}
	if !reflect.DeepEqual(targets(two), []string{"a", "b"}) {
		t.Error(targets(two))
	}
	c, _ := edit(two, Input{About: L("c")})
	if !reflect.DeepEqual(targets(c), []string{"c"}) {
		t.Error(targets(c))
	}
	cleared, _ := edit(two, Input{About: L()})
	eq(t, len(cleared), 0)
	_, err = EditRels(nil, Input{PartOf: L("t-1")}, "alice", "t-1")
	wantErr(t, err, "itself")
}

func TestEditRelsTheOldArgumentsProjectAndParentAreMapped(t *testing.T) {
	rels, err := EditRels(nil, Input{AssignedTo: S("leo"), Project: S("work/acme"), Parent: S("t-1")}, "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rels {
		got = append(got, r.Rel+":"+string(r.Kind)+":"+r.Target)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"assigned_to:page:leo", "part_of:page:work/acme", "part_of:task:t-1"}) {
		t.Error(got)
	}
	// the slug sent back changes nothing; null clears
	same, _ := EditRels(rels, Input{Project: S("work/acme")}, "alice", "")
	if !reflect.DeepEqual(same, rels) {
		t.Errorf("%+v", same)
	}
	none, _ := EditRels(rels, Input{Project: S("")}, "alice", "")
	for _, r := range none {
		if r.Target == "work/acme" {
			t.Error("project cleared")
		}
	}
	_, err = EditRels(nil, Input{Parent: S("TASK-1")}, "alice", "")
	wantErr(t, err, "task id")
}

func TestEditRelsRefusesATaskAsItsOwnParentOrBlockerInAnyCase(t *testing.T) {
	_, err := EditRels(nil, Input{BlockedBy: L("t-20261005-AAAAAA")}, "alice", "t-20261005-aaaaaa")
	wantErr(t, err, "wait for itself")
	_, err = EditRels(nil, Input{Parent: S("t-20261005-AAAAAA")}, "alice", "t-20261005-aaaaaa")
	wantErr(t, err, "part of itself")
}

// Pages in the old shape still exist in a live database: what their `owner` means depends on the account and the status.
func TestLegacyRelsTheAccountIsNothingAPersonIsWaitedOnWhileWaitingAnEngineIsAlwaysWorkedBy(t *testing.T) {
	c := func(owner string, status Status, me string) []string {
		var out []string
		for _, r := range legacyRels(oldFields{Owner: owner}, me, status) {
			out = append(out, r.Rel+":"+r.Target)
		}
		return out
	}
	eq(t, len(c("alice", Todo, "alice")), 0)
	eq(t, len(c("Alice", Waiting, "alice")), 0)
	eq(t, strings.Join(c("bob", Waiting, "alice"), ","), "waiting_on:bob")
	eq(t, strings.Join(c("bob", Doing, "alice"), ","), "assigned_to:bob")
	eq(t, strings.Join(c("claude", Waiting, "alice"), ","), "worked_by:claude")
	eq(t, strings.Join(c("bob", Waiting, ""), ","), "assigned_to:bob")
}

func roles(tk *Task) []string {
	var out []string
	for _, r := range RelsOf(*tk) {
		out = append(out, r.Rel+":"+lastPart(r.Target))
	}
	return out
}

func TestFromFileTheOldOwnerByTheAccountAndTheStatus(t *testing.T) {
	old := func(id string, status Status, owner string) string {
		return strings.Replace(oldFile(id, [][2]string{{"owner", `"` + owner + `"`}}, ""), `status: "todo"`, `status: "`+string(status)+`"`, 1)
	}
	eq(t, len(roles(FromFile(old("t-1", Todo, "alice"), "alice"))), 0)
	eq(t, FromFile(old("t-1", Todo, "alice"), "alice").Owner, "")
	eq(t, strings.Join(roles(FromFile(old("t-2", Waiting, "bob"), "alice")), ","), "waiting_on:bob")
	eq(t, strings.Join(roles(FromFile(old("t-3", Todo, "bob"), "alice")), ","), "assigned_to:bob")
	eq(t, len(roles(FromFile(old("t-4", Waiting, "alice"), "alice"))), 0)
}

func TestRelsOfAHandBuiltTaskWithoutRelsReadsItsOldFields(t *testing.T) {
	tk := Task{ID: "t-1", Owner: "bob", Project: "work/acme", Parent: "t-0", BlockedBy: []string{"t-2"}}
	eq(t, strings.Join(roles(&tk), ","), "part_of:acme,part_of:t-0,blocked_by:t-2,assigned_to:bob")
	tk.Rels = []Rel{}
	eq(t, len(RelsOf(tk)), 0) // present Rels, even empty, win over the old fields
}
