package main

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// eofGrace bounds how long a closed stdin waits for the requests already read to be answered; a variable so a test can lower it.
var eofGrace = 30 * time.Second

// stdioTransport is the SDK's stdio transport with one change: the SDK takes EOF on stdin as the peer gone and cancels
// every request in flight, so `printf '<requests>' | kedalio-core stdio` printed nothing. Here EOF is held back until
// each request read has its answer written (or eofGrace has passed), which is what a script that pipes a fixed batch needs.
func stdioTransport(in io.ReadCloser, out io.WriteCloser) sdk.Transport {
	t := &tracker{changed: make(chan struct{}, 1), pending: map[string]bool{}}
	return &sdk.IOTransport{Reader: &trackedReader{in, t}, Writer: &trackedWriter{out, t}}
}

// tracker keeps the requests read and not yet answered or cancelled. Each direction keeps its own partial line.
type tracker struct {
	changed chan struct{}

	mu      sync.Mutex
	pending map[string]bool // ids of the requests read and not answered or cancelled
	inLine  []byte
	outLine []byte
}

// feed adds the bytes of one direction and applies each complete message: a request (method and id) is one more
// pending, an answer (id, no method) or a cancellation of it (notifications/cancelled) one less: a cancelled request
// may never be answered, and EOF would wait the whole grace for it.
func (t *tracker) feed(b []byte, incoming bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	buf := &t.outLine
	if incoming {
		buf = &t.inLine
	}
	*buf = append(*buf, b...)
	for {
		i := bytes.IndexByte(*buf, '\n')
		if i < 0 {
			break
		}
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				RequestID json.RawMessage `json:"requestId"`
			} `json:"params"`
		}
		if json.Unmarshal((*buf)[:i], &m) == nil {
			switch {
			case len(m.ID) > 0 && string(m.ID) != "null":
				if incoming && m.Method != "" {
					t.pending[string(m.ID)] = true
				} else if !incoming && m.Method == "" {
					delete(t.pending, string(m.ID))
				}
			case incoming && m.Method == "notifications/cancelled":
				delete(t.pending, string(m.Params.RequestID))
			}
		}
		*buf = (*buf)[i+1:]
	}
	select {
	case t.changed <- struct{}{}:
	default:
	}
}

func (t *tracker) drain() {
	deadline := time.After(eofGrace)
	for {
		t.mu.Lock()
		pending := len(t.pending)
		t.mu.Unlock()
		if pending == 0 {
			return
		}
		select {
		case <-t.changed:
		case <-deadline:
			return
		}
	}
}

type trackedReader struct {
	io.ReadCloser
	t *tracker
}

func (r *trackedReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.t.feed(p[:n], true)
	if err == io.EOF {
		r.t.drain()
	}
	return n, err
}

type trackedWriter struct {
	io.WriteCloser
	t *tracker
}

func (w *trackedWriter) Write(p []byte) (int, error) {
	n, err := w.WriteCloser.Write(p)
	w.t.feed(p[:n], false)
	return n, err
}
