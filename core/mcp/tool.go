package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"kedalio/core/memory"
	"kedalio/core/projects"
	"kedalio/core/tasks"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// A tool is one MCP tool: its schema is written by hand, flat at the root (an operation choice is an `op` enum, never
// a root union: the Claude API rejected those), and checked here, so that a wrong argument is answered in the words
// every tool of the connector uses, `{errors: [{field, problem, fix}]}`, not in the SDK's.

// kind is what a call does: it picks the annotations clients use to decide what to ask the owner about.
type kind byte

const (
	read    kind = 'R'
	write   kind = 'W'
	destroy kind = 'D'
)

type tool struct {
	name, description string
	kind              kind
	// upFront: clients that defer tools load this one at once (`anthropic/alwaysLoad`).
	upFront bool
	schema  schema
	run     func(ctx context.Context, args []byte) (any, error)
}

// issue is one thing wrong with a call.
type issue struct {
	Field   string `json:"field"`
	Problem string `json:"problem"`
	Fix     string `json:"fix"`
}

// callError is a refusal the handler chooses: it becomes `isError` with the issues.
type callError struct{ issues []issue }

func (e *callError) Error() string {
	var parts []string
	for _, i := range e.issues {
		parts = append(parts, i.Field+": "+i.Problem)
	}
	return strings.Join(parts, "; ")
}

// needs refuses a call that lacks an argument the operation needs and the flat schema had to leave optional.
func needs(op string, fields ...field) error {
	var missing []issue
	for _, f := range fields {
		if f.empty {
			missing = append(missing, issue{f.name, "missing", "give " + f.name + " for " + op})
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return &callError{missing}
}

type field struct {
	name  string
	empty bool
}

func f(name, value string) field { return field{name, value == ""} }

func refusal(field, problem, fix string) error { return &callError{[]issue{{field, problem, fix}}} }

// schema is a JSON Schema object: properties in the order written, which is the order a client shows them.
type schema struct {
	props    []prop
	required []string
}

type prop struct {
	name string
	def  map[string]any
}

func object(required []string, props ...prop) schema { return schema{props, required} }

func (s schema) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(`{"type":"object","properties":{`)
	for i, p := range s.props {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(p.name)
		v, err := json.Marshal(p.def)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	if len(s.required) > 0 {
		r, _ := json.Marshal(s.required)
		b.WriteString(`,"required":`)
		b.Write(r)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func described(def map[string]any, desc []string) prop {
	if len(desc) > 0 && desc[0] != "" {
		def["description"] = desc[0]
	}
	return prop{def: def}
}

func named(name string, p prop) prop { p.name = name; return p }

func str(name string, desc ...string) prop {
	return named(name, described(map[string]any{"type": "string"}, desc))
}

func boolean(name string, desc ...string) prop {
	return named(name, described(map[string]any{"type": "boolean"}, desc))
}

func integer(name string, lo, hi int, desc ...string) prop {
	d := map[string]any{"type": "integer", "minimum": lo}
	if hi > 0 {
		d["maximum"] = hi
	}
	return named(name, described(d, desc))
}

func choice(name string, values []string, desc ...string) prop {
	return named(name, described(map[string]any{"type": "string", "enum": values}, desc))
}

// nullable allows null beside the type: tasks_save clears a field with it.
func nullable(p prop) prop {
	def := maps.Clone(p.def)
	def["type"] = []string{def["type"].(string), "null"}
	p.def = def
	return p
}

func strings_(name string, desc ...string) prop {
	return named(name, described(map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, desc))
}

func ints(name string, desc ...string) prop {
	return named(name, described(map[string]any{
		"type": "array", "items": map[string]any{"type": "integer", "minimum": 0}}, desc))
}

// validate checks the arguments against the schema: the types, the enums and the ranges, what zod checked before.
func (s schema) validate(args map[string]any) []issue {
	var out []issue
	for _, name := range s.required {
		if _, ok := args[name]; !ok {
			out = append(out, issue{name, "missing", "give a value of type " + s.typeOf(name)})
		}
	}
	for _, p := range s.props {
		v, ok := args[p.name]
		if !ok {
			continue
		}
		out = append(out, check(p.name, p.def, v)...)
	}
	return out
}

func (s schema) typeOf(name string) string {
	for _, p := range s.props {
		if p.name == name {
			t, _ := typeName(p.def)
			return t
		}
	}
	return "value"
}

// typeName is a definition's type, and whether null is allowed too (`type: [t, "null"]`).
func typeName(def map[string]any) (string, bool) {
	if ts, ok := def["type"].([]string); ok {
		return ts[0], true
	}
	return def["type"].(string), false
}

func check(name string, def map[string]any, v any) []issue {
	if _, typed := def["type"]; !typed { // a value of any shape (settings_set), checked by its handler
		return nil
	}
	typ, nullable := typeName(def)
	if v == nil && nullable {
		return nil
	}
	wrong := func() []issue {
		return []issue{{name, fmt.Sprintf("expected %s, received %s", typ, jsType(v)), "give a value of type " + typ}}
	}
	switch typ {
	case "string":
		s, ok := v.(string)
		if !ok {
			return wrong()
		}
		if enum, ok := def["enum"].([]string); ok && !slices.Contains(enum, s) {
			var q []string
			for _, e := range enum {
				q = append(q, fmt.Sprintf("%q", e))
			}
			return []issue{{name, "invalid option", "use one of: " + strings.Join(q, ", ")}}
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return wrong()
		}
	case "integer":
		n, ok := v.(json.Number)
		if !ok {
			return wrong()
		}
		i, err := n.Int64()
		if err != nil {
			return []issue{{name, "expected an integer", "give a whole number"}}
		}
		if lo, ok := def["minimum"].(int); ok && int(i) < lo {
			return []issue{{name, fmt.Sprintf("too small: expected a number >= %d", lo), "correct the value and call again"}}
		}
		if hi, ok := def["maximum"].(int); ok && int(i) > hi {
			return []issue{{name, fmt.Sprintf("too big: expected a number <= %d", hi), "correct the value and call again"}}
		}
	case "array":
		list, ok := v.([]any)
		if !ok {
			return wrong()
		}
		item := def["items"].(map[string]any)
		var out []issue
		for i, e := range list {
			out = append(out, check(fmt.Sprintf("%s.%d", name, i), item, e)...)
		}
		return out
	}
	return nil
}

func jsType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	case []any:
		return "array"
	default:
		return "object"
	}
}

// failure is a refused call: `isError` and the issues as text.
func failure(issues []issue) *sdk.CallToolResult {
	b, _ := json.Marshal(map[string]any{"errors": issues})
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: string(b)}}}
}

// refused is a failure whose words are masked: a refusal quotes what the caller gave, and a secret must not come back in it.
func (s *Server) refused(issues []issue) *sdk.CallToolResult {
	if s.cfg.Mask != nil {
		masked := make([]issue, len(issues))
		for i, x := range issues {
			masked[i] = issue{s.cfg.Mask(x.Field), s.cfg.Mask(x.Problem), s.cfg.Mask(x.Fix)}
		}
		issues = masked
	}
	return failure(issues)
}

// handler turns a tool into the SDK's handler: arguments checked, the answer masked and written as JSON text.
func (s *Server) handler(t *tool) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		if p := s.policyOf(ctx); p != nil {
			if no := p.allowed(t.name); no != nil {
				return s.refused(no), nil
			}
			if p.ownAuthor {
				ctx = tasks.WithAuthor(ctx, s.by(ctx))
			}
		}
		raw := req.Params.Arguments
		if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
			raw = json.RawMessage("{}")
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var args map[string]any
		if err := dec.Decode(&args); err != nil {
			return failure([]issue{{"(arguments)", "not a JSON object", "give the arguments as an object"}}), nil
		}
		if issues := t.schema.validate(args); len(issues) > 0 {
			return failure(issues), nil
		}
		out, err := t.run(ctx, raw)
		if err != nil {
			var ce *callError
			if errors.As(err, &ce) {
				return s.refused(ce.issues), nil
			}
			var cl *clientError
			var tr *tasks.RefusedError
			var pr *projects.RefusedError
			if fix, known := knownFix(err); known || errors.As(err, &cl) || errors.As(err, &tr) || errors.As(err, &pr) || memory.IsRefusal(err) {
				if !known {
					fix = "read the problem, change the call and try again"
				}
				return s.refused([]issue{{"(call)", err.Error(), fix}}), nil
			}
			// anything else is the core's own failure (a database, a disk): its text can carry paths or values the
			// client has no use for, so the owner reads it in the log (masked) and the client gets a plain refusal
			msg := err.Error()
			if s.cfg.Mask != nil {
				msg = s.cfg.Mask(msg)
			}
			slog.Error("tool call failed", "tool", t.name, "err", msg)
			return s.refused([]issue{{"(call)", "the call failed inside the core", "try again; if it keeps failing the owner finds the cause in the core's log"}}), nil
		}
		body, err := s.render(out)
		if err != nil {
			return nil, err
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: body}}}, nil
	}
}

// clientError is a plain error whose text is written for the client (the page it asked for is not there): the handler
// passes it on, where any other error is the core's own failure and stays in the log.
type clientError struct{ error }

func clientErrorf(format string, a ...any) error { return &clientError{fmt.Errorf(format, a...)} }

func (e *clientError) Unwrap() error { return e.error }

// knownFix is what to do about an error nobody wrote a refusal for but whose text is meant for the client (a missing
// task, a changed page); false for any other error, which is the core's own failure.
func knownFix(err error) (string, bool) {
	msg := err.Error()
	switch {
	case errors.Is(err, memory.ErrChanged):
		return "read the page again (memory_read), apply your change to it and pass its new rev as base_rev", true
	case errors.Is(err, context.DeadlineExceeded):
		return "the call took too long: call it again", true
	case strings.HasPrefix(msg, "no task"):
		return "list the tasks (tasks_get with no id) and use an id or ref from the list", true
	case strings.HasPrefix(msg, "no collection"):
		return "list the collections (memory_list what collections) and use an id from the list", true
	case strings.HasPrefix(msg, "no document"):
		return "find the page with memory_recall, or list a folder with memory_list", true
	case strings.HasPrefix(msg, "no version"):
		return "read the page's versions (memory_read with history true) and pick one of them", true
	case strings.HasPrefix(msg, "not a document path"):
		return "give the path as folder/name.md", true
	}
	return "", false
}

func ptr[T any](v T) *T { return &v }

// add registers a tool on the SDK server.
func (s *Server) add(srv *sdk.Server, t *tool) {
	ann := &sdk.ToolAnnotations{ReadOnlyHint: t.kind == read, OpenWorldHint: ptr(false)}
	if t.kind != read {
		ann.DestructiveHint = ptr(t.kind == destroy)
	}
	st := &sdk.Tool{Name: t.name, Description: t.description, InputSchema: t.schema, Annotations: ann}
	if t.upFront {
		st.Meta = sdk.Meta{"anthropic/alwaysLoad": true}
	}
	srv.AddTool(st, s.handler(t))
}

// decode reads the arguments into the protocol's struct for the tool.
func decode[T any](raw []byte) (T, error) {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, refusal("(arguments)", err.Error(), "correct the arguments and call again")
	}
	return v, nil
}
