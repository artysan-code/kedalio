package mcp

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestInternalErrorIsLoggedMaskedAndTheClientGetsAGenericRefusal(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	s := &Server{cfg: Config{Mask: func(x string) string { return strings.ReplaceAll(x, "hunter2", "***") }}}
	h := s.handler(&tool{name: "boom", run: func(context.Context, []byte) (any, error) {
		return nil, errors.New("sqlite: disk I/O error near hunter2")
	}})
	res, err := h(ctx, &sdk.CallToolRequest{Params: &sdk.CallToolParamsRaw{Name: "boom"}})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(*sdk.TextContent).Text
	if !res.IsError || strings.Contains(text, "disk I/O") || strings.Contains(text, "hunter2") {
		t.Errorf("client saw %q, want a refusal that does not carry the internal error", text)
	}
	if got := logs.String(); !strings.Contains(got, "disk I/O error") || strings.Contains(got, "hunter2") || !strings.Contains(got, "boom") {
		t.Errorf("log = %q, want the masked error and the tool name", got)
	}
}

func TestKnownErrorsKeepTheirMessageForTheClient(t *testing.T) {
	s := &Server{}
	h := s.handler(&tool{name: "get", run: func(context.Context, []byte) (any, error) {
		return nil, errors.New("no task t-9")
	}})
	res, _ := h(ctx, &sdk.CallToolRequest{Params: &sdk.CallToolParamsRaw{Name: "get"}})
	if text := res.Content[0].(*sdk.TextContent).Text; !strings.Contains(text, "no task t-9") {
		t.Errorf("client saw %q, want the known message", text)
	}
}
