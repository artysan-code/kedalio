package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// A script that pipes its requests and closes stdin must still get every answer: the SDK treats EOF as the peer gone and
// cancels what is in flight.
func TestStdioAnswersRequestsThatArriveJustBeforeEOF(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "t", Version: "1"}, nil)
	type none struct{}
	sdk.AddTool(srv, &sdk.Tool{Name: "slow"}, func(ctx context.Context, _ *sdk.CallToolRequest, _ none) (*sdk.CallToolResult, none, error) {
		time.Sleep(100 * time.Millisecond)
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "done"}}}, none{}, nil
	})
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"c","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"slow","arguments":{}}}`,
	}, "\n") + "\n"
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
	for _, id := range []string{`"id":1`, `"id":2`, `"id":3`} {
		if !strings.Contains(out.String(), id) {
			t.Errorf("no answer with %s in %q", id, out.String())
		}
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// A request the client cancels may never be answered: it must not keep EOF waiting for the whole grace.
func TestTrackerForgetsACancelledRequest(t *testing.T) {
	tr := &tracker{changed: make(chan struct{}, 1), pending: map[string]bool{}}
	tr.feed([]byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call"}`+"\n"), true)
	tr.feed([]byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7}}`+"\n"), true)
	if n := len(tr.pending); n != 0 {
		t.Fatalf("%d requests pending after the cancellation", n)
	}
}
