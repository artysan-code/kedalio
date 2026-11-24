package memory

import (
	"reflect"
	"testing"
)

func TestFrontMatter(t *testing.T) {
	got := FrontMatter("---\nkind: entity\nis: \"person\"\n---\n# Ann")
	if want := map[string]string{"kind": "entity", "is": "person"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
	if got := FrontMatter("# Ann\n\n---\nkind: x\n---"); len(got) != 0 {
		t.Errorf("no front matter expected, got %v", got)
	}
}

func TestSetFrontMatter(t *testing.T) {
	if got, want := SetFrontMatter("# Ann\n", FMValue{"kind", "entity"}), "---\nkind: entity\n---\n# Ann\n"; got != want {
		t.Errorf("got %q", got)
	}
	got := SetFrontMatter("---\ntitle: Ann\nkind: concept\n---\n# Ann\n", FMValue{"kind", "entity"}, FMValue{"is", "person"})
	if want := "---\ntitle: Ann\nkind: entity\nis: person\n---\n# Ann\n"; got != want {
		t.Errorf("got %q", got)
	}
}

func TestInferKind(t *testing.T) {
	for _, c := range []struct {
		path  string
		areas Areas
		want  KindIs
	}{
		{"persone/ann.md", AreasIT, KindIs{KindEntity, "person"}},
		{"clients/acme.md", AreasEN, KindIs{KindEntity, "organization"}},
		{"progetti/work/acme/site.md", AreasIT, KindIs{KindEntity, "project"}},
		{"diario/2026-10-25.md", AreasIT, KindIs{Kind: KindEpisode}},
		{"note/oauth.md", AreasIT, KindIs{Kind: KindConcept}},
		{"tabletop-rpg.md", AreasIT, KindIs{Kind: KindConcept}},
	} {
		if got := InferKind(c.path, c.areas); got != c.want {
			t.Errorf("%s: got %v, want %v", c.path, got, c.want)
		}
	}
}

func TestWithKind(t *testing.T) {
	said := "---\nkind: source\n---\n# Manual\n"
	k, body := WithKind("note/manual.md", said, AreasIT, nil)
	if k != (KindIs{Kind: KindSource}) || body != said {
		t.Errorf("said: %v %q", k, body)
	}
	k, body = WithKind("note/alice.md", "# Alice\n", AreasIT, &KindIs{KindEntity, "person"})
	if k != (KindIs{KindEntity, "person"}) || body != "---\nkind: entity\nis: person\n---\n# Alice\n" {
		t.Errorf("was: %v %q", k, body)
	}
	_, body = WithKind("persone/bob.md", "# Bob\n", AreasIT, nil)
	if body != "---\nkind: entity\nis: person\n---\n# Bob\n" {
		t.Errorf("folder: %q", body)
	}
	_, body = WithKind("note/x.md", "---\nkind: persona\n---\n# X\n", AreasIT, nil)
	if body != "---\nkind: concept\n---\n# X\n" {
		t.Errorf("unknown kind: %q", body)
	}
	// a writer's own `is` stays
	_, body = WithKind("persone/bob.md", "---\nis: friend\n---\n# Bob\n", AreasIT, nil)
	if body != "---\nis: friend\nkind: entity\n---\n# Bob\n" {
		t.Errorf("own is: %q", body)
	}
}
