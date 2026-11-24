package tasks

import (
	"reflect"
	"testing"
)

func TestIsLogPathAndTaskIDOfTellALogEpisodeFromATaskPage(t *testing.T) {
	for _, c := range []struct {
		path string
		log  bool
		id   string
	}{
		{"tasks/t-abc12.md", false, "t-abc12"},
		{"tasks/t-abc12/log.md", true, ""},
		{"tasks/t-a-b/log.md", true, ""},
		{"tasks/t-abc12/other.md", false, ""},
		{"notes/t-abc12.md", false, ""},
		{"tasks/abc12.md", false, ""},
		{"tasks/t-abc12.md.bak", false, ""},
		{"", false, ""},
	} {
		if got := IsLogPath(c.path); got != c.log {
			t.Errorf("IsLogPath(%q) = %v", c.path, got)
		}
		if got := TaskIDOf(c.path); got != c.id {
			t.Errorf("TaskIDOf(%q) = %q, want %q", c.path, got, c.id)
		}
	}
}

func TestSummaryOfIsTheSectionAsWrittenUpToTheNextHeading(t *testing.T) {
	for _, c := range []struct{ name, body, want string }{
		{"none", "# Log\n\n## Log\n\n- x\n", ""},
		{"english", "# Log\n\n## Summary\n\nDone it.\n\n## Log\n\n- x\n", "## Summary\n\nDone it."},
		{"italian, upper case", "## RIASSUNTO\nfatto\n\n## Registro\n- x\n", "## RIASSUNTO\nfatto"},
		{"to the end", "## Summary\nline one\nline two\n\n\n", "## Summary\nline one\nline two"},
		{"front matter is not read", "---\ntitle: T\n---\n## Summary\nhi\n", "## Summary\nhi"},
		{"a deeper heading stays inside", "## Summary\n### Why\nbecause\n## Log\n", "## Summary\n### Why\nbecause"},
	} {
		if got := summaryOf(c.body); got != c.want {
			t.Errorf("%s: summaryOf = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestOtherOfIsWhoElseTheTaskIsWithAndNeverTheOwner(t *testing.T) {
	task := func(rels ...Rel) Task { return Task{Rels: rels} }
	page := func(rel, target string) Rel { return Rel{Rel: rel, Kind: KindPage, Target: target} }
	for _, c := range []struct {
		name string
		t    Task
		want string
	}{
		{"no relation", task(), ""},
		{"waiting on someone", task(page("waiting_on", "persone/bob")), "bob"},
		{"assigned to the owner by name", task(page("assigned_to", "Alice")), ""},
		{"assigned to the owner's area", task(page("assigned_to", "persone/alice/team")), ""},
		{"for someone else", task(page("for", "persone/carol.md")), "carol"},
		{"the first one that is not the owner's", task(page("assigned_to", "alice"), page("for", "dave")), "dave"},
		{"an empty target", task(page("waiting_on", "")), ""},
	} {
		if got := OtherOf(c.t, "alice", "persone/alice"); got != c.want {
			t.Errorf("%s: OtherOf = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestLikeEscapeKeepsWildcardsLiteral(t *testing.T) {
	for in, want := range map[string]string{
		"plain":   "plain",
		"100%":    `100\%`,
		"a_b":     `a\_b`,
		`a\b`:     `a\\b`,
		`%_\`:     `\%\_\\`,
		"":        "",
		"città_1": `città\_1`,
	} {
		if got := likeEscape(in); got != want {
			t.Errorf("likeEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUtf16PrefixCountsAnAstralCharacterAsTwoUnitsAndNeverSplitsIt(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"abc", 2, "ab"},
		{"abc", 10, "abc"},
		{"abc", 0, ""},
		{"a😀b", 3, "a😀"},
		{"a😀b", 2, "a"}, // the pair would end past the cut: it is dropped whole
		{"😀😀", 3, "😀"},
		{"è😀", 1, "è"},
	} {
		if got := string(utf16Prefix(c.in, c.n)); got != c.want {
			t.Errorf("utf16Prefix(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestSlugOfProjectDropsTheProjectsAreaInEitherLanguage(t *testing.T) {
	for in, want := range map[string]string{
		"progetti/kedalio":       "kedalio",
		"projects/kedalio.md":    "kedalio",
		"projects/a/b":           "a/b",
		"PROJECTS/x.MD":          "PROJECTS/x", // the area is a folder name: case matters
		"kedalio":                "kedalio",
		"progettiX/kedalio":      "progettiX/kedalio",
		"altro/progetti/kedalio": "altro/progetti/kedalio",
	} {
		if got := slugOfProject(in); got != want {
			t.Errorf("slugOfProject(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeriveSetsTheOldFieldsFromTheRelationsAndClearsThemWhenThereAreNone(t *testing.T) {
	task := &Task{
		Project: "stale", Parent: "t-stale", Owner: "stale", BlockedBy: []string{"t-stale"},
		Rels: []Rel{
			{Rel: "part_of", Kind: KindPage, Target: "progetti/kedalio"},
			{Rel: "part_of", Kind: KindPage, Target: "progetti/second"},
			{Rel: "part_of", Kind: KindTask, Target: "t-parent"},
			{Rel: "blocked_by", Kind: KindTask, Target: "t-one"},
			{Rel: "blocked_by", Kind: KindTask, Target: "t-two"},
			{Rel: "blocked_by", Kind: KindPage, Target: "not-a-task"},
			{Rel: "assigned_to", Kind: KindPage, Target: "persone/Bob.md"},
			{Rel: "worked_by", Kind: KindRun, Target: "r-1"},
		},
	}
	derive(task)
	if task.Project != "kedalio" || task.Parent != "t-parent" || task.Owner != "bob" ||
		!reflect.DeepEqual(task.BlockedBy, []string{"t-one", "t-two"}) {
		t.Errorf("%+v", task)
	}

	byRun := derive(&Task{Rels: []Rel{{Rel: "worked_by", Kind: KindRun, Target: "r-1"}}})
	if byRun.Owner != "claude" {
		t.Errorf("a run works as claude: %q", byRun.Owner)
	}
	byAgent := derive(&Task{Rels: []Rel{{Rel: "worked_by", Kind: KindAgent, Target: "scout"}}})
	if byAgent.Owner != "scout" {
		t.Errorf("an agent works as itself: %q", byAgent.Owner)
	}

	bare := derive(&Task{Rels: []Rel{}, Project: "stale", Parent: "t-stale", Owner: "stale", BlockedBy: []string{"t-stale"}})
	if bare.Project != "" || bare.Parent != "" || bare.Owner != "" || bare.BlockedBy != nil {
		t.Errorf("not cleared: %+v", bare)
	}
}
