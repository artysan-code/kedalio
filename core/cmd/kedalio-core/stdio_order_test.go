package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// runStdioBatch pipes the lines to a server and returns the ids of the answers in the order they were written.
func runStdioBatch(t *testing.T, srv *sdk.Server, lines ...string) []string {
	t.Helper()
	return runStdioRaw(t, srv, strings.Join(lines, "\n")+"\n")
}

// runStdioRaw is runStdioBatch with the input exactly as given.
func runStdioRaw(t *testing.T, srv *sdk.Server, in string) []string {
	t.Helper()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- srv.Run(context.Background(), stdioTransport(io.NopCloser(strings.NewReader(in)), nopWriteCloser{&out}))
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not end after stdin closed")
	}
	var ids []string
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal([]byte(l), &m) == nil && len(m.ID) > 0 {
			ids = append(ids, string(m.ID))
		}
	}
	return ids
}

const (
	stdioInit   = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"c","version":"1"}}}`
	stdioInited = `{"jsonrpc":"2.0","method":"notifications/initialized"}`
)

func stdioCall(id int, tool string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":{}}}`, id, tool)
}

type noArgs struct{}

func textResult(s string) *sdk.CallToolResult {
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: s}}}
}

// Requests on one stream are handled in the order they arrive: a read sent right after a write must see it, and the
// answers must not overtake each other. The SDK runs every call in its own goroutine.
func TestStdioHandlesRequestsInArrivalOrder(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "t", Version: "1"}, nil)
	var mu sync.Mutex
	value, seen := "unset", ""
	sdk.AddTool(srv, &sdk.Tool{Name: "save"}, func(context.Context, *sdk.CallToolRequest, noArgs) (*sdk.CallToolResult, noArgs, error) {
		time.Sleep(150 * time.Millisecond)
		mu.Lock()
		value = "saved"
		mu.Unlock()
		return textResult("ok"), noArgs{}, nil
	})
	sdk.AddTool(srv, &sdk.Tool{Name: "get"}, func(context.Context, *sdk.CallToolRequest, noArgs) (*sdk.CallToolResult, noArgs, error) {
		mu.Lock()
		seen = value
		mu.Unlock()
		return textResult(seen), noArgs{}, nil
	})
	ids := runStdioBatch(t, srv, stdioInit, stdioInited, stdioCall(2, "save"), stdioCall(3, "get"))
	if got := strings.Join(ids, ","); got != "1,2,3" {
		t.Errorf("answers in order %s, want 1,2,3", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if seen != "saved" {
		t.Errorf("the read ran before the write finished: saw %q", seen)
	}
}

// A cancellation must not wait behind the request it cancels, nor behind the ones held back after it.
func TestStdioCancellationOvertakesAHeldBackRequest(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "t", Version: "1"}, nil)
	sdk.AddTool(srv, &sdk.Tool{Name: "block"}, func(ctx context.Context, _ *sdk.CallToolRequest, _ noArgs) (*sdk.CallToolResult, noArgs, error) {
		<-ctx.Done()
		return nil, noArgs{}, ctx.Err()
	})
	sdk.AddTool(srv, &sdk.Tool{Name: "fast"}, func(context.Context, *sdk.CallToolRequest, noArgs) (*sdk.CallToolResult, noArgs, error) {
		return textResult("ok"), noArgs{}, nil
	})
	ids := runStdioBatch(t, srv, stdioInit, stdioInited, stdioCall(2, "block"), stdioCall(3, "fast"),
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":2}}`)
	if !slices.Contains(ids, "3") {
		t.Errorf("the request after a cancelled one was never answered: %v", ids)
	}
}

func fastTool(srv *sdk.Server) {
	sdk.AddTool(srv, &sdk.Tool{Name: "fast"}, func(context.Context, *sdk.CallToolRequest, noArgs) (*sdk.CallToolResult, noArgs, error) {
		return textResult("ok"), noArgs{}, nil
	})
}

// The client's answer to a request of the server (roots/list) must not wait behind the next call: the call before it is
// waiting for exactly that answer.
func TestStdioClientAnswersPassWhileACallIsHeldBack(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "t", Version: "1"}, nil)
	sdk.AddTool(srv, &sdk.Tool{Name: "roots"}, func(ctx context.Context, req *sdk.CallToolRequest, _ noArgs) (*sdk.CallToolResult, noArgs, error) {
		time.Sleep(200 * time.Millisecond) // the next call is read meanwhile
		//lint:ignore SA1019 any server-to-client request will do; roots/list is the simplest one the SDK offers
		if _, err := req.Session.ListRoots(ctx, nil); err != nil {
			return nil, noArgs{}, err
		}
		return textResult("ok"), noArgs{}, nil
	})
	fastTool(srv)
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	go func() { _ = srv.Run(context.Background(), stdioTransport(c2sR, s2cW)) }()
	send := func(line string) {
		if _, err := io.WriteString(c2sW, line+"\n"); err != nil {
			t.Error(err)
		}
	}
	// a client by hand: it answers roots/list only after the second call is sent
	send(strings.Replace(stdioInit, `"capabilities":{}`, `"capabilities":{"roots":{}}`, 1))
	send(stdioInited)
	send(stdioCall(2, "roots"))
	time.Sleep(50 * time.Millisecond)
	send(stdioCall(3, "fast"))
	answered := map[string]bool{}
	deadline := time.AfterFunc(5*time.Second, func() { _ = s2cR.Close() })
	defer deadline.Stop()
	sc := bufio.NewScanner(s2cR)
	for !(answered["2"] && answered["3"]) && sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if m.Method == "roots/list" {
			send(`{"jsonrpc":"2.0","id":` + string(m.ID) + `,"result":{"roots":[]}}`)
		} else if m.Method == "" {
			answered[string(m.ID)] = true
		}
	}
	if !answered["2"] || !answered["3"] {
		t.Errorf("answered %v: the answer to roots/list was held back behind a call", answered)
	}
}

// The last line of the input may come without a newline.
func TestStdioServesALastLineWithoutNewline(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "t", Version: "1"}, nil)
	fastTool(srv)
	ids := runStdioRaw(t, srv, stdioInit+"\n"+stdioInited+"\n"+stdioCall(2, "fast"))
	if got := strings.Join(ids, ","); got != "1,2" {
		t.Errorf("answers %s, want 1,2", got)
	}
}

// The SDK writes the id back in its own form (2.0 as 2): the next call must not wait for an answer that is never matched.
func TestStdioMatchesAnswersWhoseIDIsWrittenDifferently(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "t", Version: "1"}, nil)
	fastTool(srv)
	call := func(id string) string {
		return `{"jsonrpc":"2.0","id":` + id + `,"method":"tools/call","params":{"name":"fast","arguments":{}}}`
	}
	ids := runStdioBatch(t, srv, stdioInit, stdioInited, call("2.0"), call(`"a\/b"`), call("4"))
	if len(ids) != 4 {
		t.Errorf("answers %v, want 4", ids)
	}
}

// Closed stdin must not wait for ever for a call held back behind one that never answers.
func TestStdioEOFGraceBoundsAHeldBackCall(t *testing.T) {
	old := eofGrace
	eofGrace = 200 * time.Millisecond
	t.Cleanup(func() { eofGrace = old })
	srv := sdk.NewServer(&sdk.Implementation{Name: "t", Version: "1"}, nil)
	sdk.AddTool(srv, &sdk.Tool{Name: "block"}, func(ctx context.Context, _ *sdk.CallToolRequest, _ noArgs) (*sdk.CallToolResult, noArgs, error) {
		<-ctx.Done()
		return nil, noArgs{}, ctx.Err()
	})
	fastTool(srv)
	runStdioBatch(t, srv, stdioInit, stdioInited, stdioCall(2, "block"), stdioCall(3, "fast"))
}
