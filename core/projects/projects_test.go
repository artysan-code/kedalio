package projects

import (
	"context"
	"slices"
	"testing"

	"kedalio/core/db"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := Migrate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	return New(d)
}

func TestSameRemoteIsOneStringForEveryForm(t *testing.T) {
	want := "git.example.com/acme/site"
	for _, in := range []string{
		"git@git.example.com:acme/site.git", "https://git.example.com/acme/site", "https://user:pw@git.example.com/acme/site.git/",
		"ssh://git@git.example.com:2222/acme/site.git", "HTTPS://GIT.EXAMPLE.COM/Acme/Site",
	} {
		if got := SameRemote(in); got != want {
			t.Errorf("%s -> %s", in, got)
		}
	}
	if RepoAddress("git@git.example.com:acme/site.git") != "https://git.example.com/acme/site" || RepoAddress("nonsense") != "" {
		t.Error("repo address")
	}
}

func TestResolveTakesTheDeepestFolderThenTheRepo(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	for _, p := range []struct{ kind, value, path string }{
		{"folder", "/h/work/acme", "work/acme"}, {"folder", "/h/work/acme/site", "work/acme/site"},
		{"repo", "https://git.example.com/acme/other", "work/acme/other"},
	} {
		if err := s.AddPlace(ctx, p.path, p.kind, p.value, ""); err != nil {
			t.Fatal(err)
		}
	}
	for dir, want := range map[string]string{"/h/work/acme/site/src": "work/acme/site", "/h/work/acme/docs": "work/acme", "/h/work/acmeX": "", "/h/work/acme/": "work/acme"} {
		if got, _ := s.Resolve(ctx, dir, ""); got != want {
			t.Errorf("%s -> %q, want %q", dir, got, want)
		}
	}
	if got, _ := s.Resolve(ctx, "/elsewhere", "git@git.example.com:acme/other.git"); got != "work/acme/other" {
		t.Errorf("by repo: %q", got)
	}
}

func TestProposeFromTheFolderAndTheRemote(t *testing.T) {
	known := map[string]bool{"work/acme": true}
	for _, c := range []struct {
		dir, remote, want, why string
	}{
		{"/h/work/acme/shop", "", "work/acme/shop", "path"},
		{"/h/work/acme/shop/shop-api", "", "work/acme/shop", "path"},
		{"/h/work/acme", "", "work/acme", "path"},
		{"/h/code/blog", "git@git.example.com:me/Blog.git", "personal/blog", "name"},
		{"/h", "", "", ""},
		{"/h/work", "", "", ""},
	} {
		p := Propose(c.dir, c.remote, known, nil)
		switch {
		case c.want == "" && p != nil:
			t.Errorf("%s: %v", c.dir, p)
		case c.want != "" && (p == nil || p.Path != c.want || p.Why != c.why):
			t.Errorf("%s: %v, want %s", c.dir, p, c.want)
		}
	}
	held := map[string]string{"git.example.com/me/blog": "personal/journal"}
	if p := Propose("/h/code/blog", "https://git.example.com/me/blog", map[string]bool{"personal/journal": true}, held); p == nil || p.Path != "personal/journal" || p.Why != "remote" || !p.Exists {
		t.Errorf("by remote: %v", p)
	}
}

func TestNearbyOrdersAncestorsSiblingsChildren(t *testing.T) {
	known := []string{"work", "work/acme", "work/acme/api", "work/acme/site/blog", "work/zed/x", "personal/a"}
	got := Nearby("work/acme/site", known, 3)
	if !slices.Equal(got, []string{"work/acme", "work", "work/acme/api"}) {
		t.Errorf("%v", got)
	}
}

func TestSettingsInheritDownThePathAndNullClears(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.SetSetting(ctx, "work/acme", "session", []byte(`{"mode":"work"}`)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.SettingOf(ctx, "work/acme/site", "session"); got.Origin != "work/acme" || string(got.Value) != `{"mode":"work"}` {
		t.Errorf("%+v", got)
	}
	if got, _ := s.SettingOf(ctx, "work/acme", "session"); got.Origin != "own" {
		t.Errorf("%+v", got)
	}
	if got, _ := s.SettingOf(ctx, "personal/x", "session"); got.Origin != "default" || string(got.Value) != "null" {
		t.Errorf("%+v", got)
	}
	_ = s.SetSetting(ctx, "work/acme", "session", []byte("null"))
	if got, _ := s.SettingOf(ctx, "work/acme/site", "session"); got.Origin != "default" {
		t.Errorf("%+v", got)
	}
}

func TestIgnoredFoldersAndRepoRefusal(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	_ = s.Ignore(ctx, "/h/tmp/")
	if ok, _ := s.Ignored(ctx, "/h/tmp"); !ok {
		t.Error("ignored")
	}
	if err := s.SetRepo(ctx, "a", "https://git.example.com/x/y"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRepo(ctx, "b", "https://git.example.com/x/y"); err == nil {
		t.Error("a repository is one project's")
	}
	if err := s.SetBranch(ctx, "nope", "main"); err == nil {
		t.Error("a branch needs a repository")
	}
}
