package memory

import (
	"reflect"
	"testing"
)

func TestRelName(t *testing.T) {
	for in, want := range map[string]string{
		"Plays with": "plays_with", "plays-with": "plays_with", "Lavora per": "lavora_per",
		"è cliente di": "e_cliente_di", "Café Ñandú": "cafe_nandu", "--": "",
	} {
		if got := RelName(in); got != want {
			t.Errorf("RelName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEdgesOf(t *testing.T) {
	body := "---\nkind: concept\n---\n# Tabletop RPG\n\nA game the owner plays, see [[campaign]] and [[alice]].\n\n" +
		"## Relations\n\n- plays_with [[alice]]\n- Uses [[rules/5e|the 2024 rules]] (since 2025)\n- [[bob]]\n" +
		"- not a relation line\n\n## Notes\n\n- uses [[elsewhere]]\n"
	want := []Edge{
		{Target: "alice", Rel: "plays_with"},
		{Target: "rules/5e", Rel: "uses", Note: "since 2025"},
		{Target: "campaign"},
		{Target: "alice"},
		{Target: "bob"},
		{Target: "elsewhere"},
	}
	if got := EdgesOf(body); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	got := EdgesOf("# X\n\n## Relazioni\n\n- gioca con [[alice]]\n")
	if !reflect.DeepEqual(got, []Edge{{Target: "alice", Rel: "gioca_con"}}) {
		t.Errorf("got %+v", got)
	}
}

func TestEndedBefore(t *testing.T) {
	for _, c := range []struct {
		note string
		want bool
	}{
		{"fino al 2026-09", true},
		{"until 2026-10-26", true},
		{"until 2026-10-27", false},
		{"fino al 2026-10", false},
		{"since 2025", false},
		{"", false},
	} {
		if got := EndedBefore(c.note, "2026-10-27"); got != c.want {
			t.Errorf("EndedBefore(%q) = %v", c.note, got)
		}
	}
}
