package instance

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// stdioRig serves the instance over pipes the way kedalio-core stdio does over stdin and stdout.
type stdioRig struct {
	in   io.WriteCloser
	out  *bufio.Reader
	done chan error
}

func newStdioRig(t *testing.T, o Options) *stdioRig {
	t.Helper()
	o.Stdio = true
	ctx, stop := context.WithCancel(context.Background())
	inst, err := o.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	r := &stdioRig{in: inW, out: bufio.NewReader(outR), done: make(chan error, 1)}
	go func() { r.done <- inst.Serve(ctx, &sdk.IOTransport{Reader: inR, Writer: outW}) }()
	t.Cleanup(func() {
		inW.Close() // stdin EOF ends the session
		select {
		case <-r.done:
		case <-time.After(10 * time.Second):
			t.Error("Serve did not return at stdin EOF")
		}
		stop()
		if err := inst.Close(); err != nil {
			t.Error(err)
		}
	})
	return r
}

// call sends one request and returns its result; the line read back must be a JSON-RPC message.
func (r *stdioRig) call(t *testing.T, id int, method string, params any) map[string]any {
	t.Helper()
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if _, err := r.in.Write(append(req, '\n')); err != nil {
		t.Fatal(err)
	}
	line, err := r.out.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var msg map[string]any
	if err := json.Unmarshal([]byte(line), &msg); err != nil || msg["jsonrpc"] != "2.0" {
		t.Fatalf("stdout carries something that is not JSON-RPC: %q (%v)", line, err)
	}
	if msg["error"] != nil {
		t.Fatalf("%s: %v", method, msg["error"])
	}
	return msg["result"].(map[string]any)
}

func (r *stdioRig) tool(t *testing.T, id int, name string, args map[string]any) string {
	t.Helper()
	res := r.call(t, id, "tools/call", map[string]any{"name": name, "arguments": args})
	if res["isError"] == true {
		t.Fatalf("%s failed: %v", name, res)
	}
	b, _ := json.Marshal(res)
	return string(b)
}

func TestStdioServesTheToolsAsTheOwnerWithNothingButJSONRPCOnTheWire(t *testing.T) {
	dir := t.TempDir()
	r := newStdioRig(t, Options{Data: dir, Language: "English", BackupEvery: time.Hour, BackupKeep: 3})

	init := r.call(t, 1, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "test", "version": "0"}})
	if info, _ := init["serverInfo"].(map[string]any); info["name"] != "kedalio-core" {
		t.Errorf("serverInfo = %v", init["serverInfo"])
	}
	if _, err := r.in.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	tools, _ := json.Marshal(r.call(t, 2, "tools/list", map[string]any{}))
	for _, name := range []string{"memory_write", "memory_recall", "tasks_save"} {
		if !strings.Contains(string(tools), `"`+name+`"`) {
			t.Errorf("tools/list lacks %s", name)
		}
	}
	r.tool(t, 3, "memory_write", map[string]any{"op": "write", "path": "notes/stamps.md", "body": "# Stamps\n\nAlice keeps her okapi stamps in the blue drawer.\n", "base_rev": 0})
	if got := r.tool(t, 4, "memory_recall", map[string]any{"query": "okapi stamps"}); !strings.Contains(got, "blue drawer") {
		t.Errorf("recall did not find the write: %s", got)
	}
	// the first start made the recovery file, and it says what the code is for without a passkey
	b, err := os.ReadFile(filepath.Join(dir, "first-run.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "Recovery code: ") || strings.Contains(string(b), "Setup link") {
		t.Errorf("first-run.txt = %q", b)
	}
	if !strings.Contains(string(b), "key file") {
		t.Errorf("first-run.txt does not say the code unlocks the database when the key file is lost: %q", b)
	}
}
