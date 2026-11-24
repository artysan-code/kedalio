package memory

import (
	"regexp"
	"strings"
)

// Kind is what a page is: a `kind` in its front matter, one of five. `task` is a task page's own.
type Kind string

const (
	KindEntity  Kind = "entity"
	KindConcept Kind = "concept"
	KindEpisode Kind = "episode"
	KindSource  Kind = "source"
	KindTask    Kind = "task"
)

// IsKind reports whether k is one of the five kinds.
func IsKind(k string) bool {
	switch Kind(k) {
	case KindEntity, KindConcept, KindEpisode, KindSource, KindTask:
		return true
	}
	return false
}

// Areas are the folders the diary and the inbox live in, and the hint for the kind of a page that does not say it.
// Their names depend on the account's language.
type Areas struct {
	Self, Projects, Clients, People, Notes, Diary, Inbox string
}

// AreasIT are the Italian names, as the brain began (its pages never move); AreasEN serve every other language.
var (
	AreasIT = Areas{"io", "progetti", "clienti", "persone", "note", "diario", "inbox"}
	AreasEN = Areas{"me", "projects", "clients", "people", "notes", "diary", "inbox"}
)

var (
	fmRe   = regexp.MustCompile(`^---\n((?s:.*?))\n---\n?`)
	fmLine = regexp.MustCompile(`^([A-Za-z_][\w-]*):\s*(.*?)\s*$`)
	quoted = regexp.MustCompile(`^["'](.*)["']$`)
)

// FrontMatter returns the front matter's `key: value` lines, values as written (quotes taken off).
func FrontMatter(body string) map[string]string {
	out := map[string]string{}
	m := fmRe.FindStringSubmatch(body)
	if m == nil {
		return out
	}
	for _, l := range strings.Split(m[1], "\n") {
		if kv := fmLine.FindStringSubmatch(l); kv != nil {
			out[kv[1]] = quoted.ReplaceAllString(kv[2], "$1")
		}
	}
	return out
}

// FMValue is one `key: value` to set in a front matter; a slice of them keeps the order keys are added in.
type FMValue struct{ Key, Value string }

// SetFrontMatter returns the body with the values set in its front matter (added, or replaced in place), the rest
// untouched.
func SetFrontMatter(body string, values ...FMValue) string {
	m := fmRe.FindString(body)
	var lines []string
	if m != "" {
		lines = strings.Split(fmRe.FindStringSubmatch(body)[1], "\n")
	}
	for _, v := range values {
		found := false
		for i, l := range lines {
			if strings.HasPrefix(l, v.Key+":") {
				lines[i] = v.Key + ": " + v.Value
				found = true
				break
			}
		}
		if !found {
			lines = append(lines, v.Key+": "+v.Value)
		}
	}
	rest := strings.TrimLeft(body, "\n")
	if m != "" {
		rest = body[len(m):]
	}
	return "---\n" + strings.Join(lines, "\n") + "\n---\n" + rest
}

// KindIs is a kind with the optional free word that says what an entity is (person, project, organization…).
type KindIs struct {
	Kind Kind
	Is   string
}

// InferKind returns the kind a page's place suggests, for a page that does not say it: the old areas in either
// language.
func InferKind(path string, a Areas) KindIs {
	area := strings.SplitN(path, "/", 2)[0]
	switch area {
	case a.People:
		return KindIs{KindEntity, "person"}
	case a.Clients:
		return KindIs{KindEntity, "organization"}
	case a.Projects:
		return KindIs{KindEntity, "project"}
	case a.Self:
		return KindIs{KindEntity, "owner"}
	case a.Diary, a.Inbox:
		return KindIs{Kind: KindEpisode}
	}
	return KindIs{Kind: KindConcept}
}

// WithKind returns a page's kind as written, else as it was (`was`, the current version's), else from its place; and
// its body with that kind in the front matter. An unknown word in `kind` is not refused: the kind comes from the rest.
func WithKind(path, body string, a Areas, was *KindIs) (k KindIs, newBody string) {
	fm := FrontMatter(body)
	// only a task page (tasks/, written by the task book) is of kind task; on any other page the word is no kind
	var said *KindIs
	if IsKind(fm["kind"]) && Kind(fm["kind"]) != KindTask {
		said = &KindIs{Kind(fm["kind"]), fm["is"]}
	}
	switch {
	case said != nil:
		k = *said
	case was != nil:
		k = *was
	default:
		k = InferKind(path, a)
	}
	values := []FMValue{{"kind", string(k.Kind)}}
	if k.Is != "" && fm["is"] == "" {
		values = append(values, FMValue{"is", k.Is})
	}
	if said != nil && (k.Is == "" || fm["is"] != "") {
		newBody = body
	} else {
		newBody = SetFrontMatter(body, values...)
	}
	if fm["is"] != "" {
		k.Is = fm["is"]
	}
	return k, newBody
}
