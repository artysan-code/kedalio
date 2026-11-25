package main

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"slices"
	"strconv"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// eofGrace bounds how long a closed stdin waits for the requests already read to be answered; a variable so a test can lower it.
var eofGrace = 30 * time.Second

// stdioTransport is the SDK's stdio transport with two changes.
//
// The SDK takes EOF on stdin as the peer gone and cancels every request in flight, so `printf '<requests>' | kedalio-core stdio`
// printed nothing. Here EOF is held back until each request read has its answer written (or eofGrace has passed), which is
// what a script that pipes a fixed batch needs.
//
// The SDK also runs every call in a goroutine of its own, so calls sent back to back were answered out of order and a read
// could run before the write sent just before it. Here a call is handed to the SDK only when the one before it is answered
// (or cancelled); notifications/cancelled go through at once.
func stdioTransport(in io.ReadCloser, out io.WriteCloser) sdk.Transport {
	t := &tracker{changed: make(chan struct{}, 1), pending: map[string]bool{}, running: map[string]bool{}}
	r := &trackedReader{in: in, t: t}
	go r.pump()
	return &sdk.IOTransport{Reader: r, Writer: &trackedWriter{out, t}}
}

// held is one input line waiting for its turn.
type held struct {
	line   []byte
	id     string
	call   bool // a request: waits until the one before it is answered
	cancel bool // notifications/cancelled: never waits, or it could not stop the request it is for
	// ordered is a call or a notification: it keeps its place in the queue. The client's answers to requests of the server
	// (roots/list, sampling) and lines that are not JSON-RPC never wait: the call before them may be waiting for them.
	ordered bool
}

// canonID is the id in the form the SDK writes it back (2.0 as 2, escapes normalised), so a request read and its answer
// written are the same key.
func canonID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return string(raw)
	}
	if n, ok := v.(json.Number); ok {
		if i, err := n.Int64(); err == nil {
			return strconv.FormatInt(i, 10)
		}
		if f, err := n.Float64(); err == nil && f == math.Trunc(f) && math.Abs(f) < 1<<53 {
			return strconv.FormatInt(int64(f), 10)
		}
		return n.String()
	}
	b, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(b)
}

// tracker keeps the requests read and not yet answered or cancelled. Each direction keeps its own partial line.
type tracker struct {
	changed chan struct{}

	mu      sync.Mutex
	pending map[string]bool // ids of the requests read and not answered or cancelled
	running map[string]bool // ids of the requests handed to the SDK and not answered or cancelled
	queue   []held          // lines read and not yet handed to the SDK
	eof     bool            // stdin is closed: no line will be added to queue
	readErr error           // why stdin ended, when it was not EOF
	inLine  []byte
	outLine []byte
}

// feed adds the bytes of one direction and applies each complete message: a request (method and id) is one more
// pending, an answer (id, no method) or a cancellation of it (notifications/cancelled) one less: a cancelled request
// may never be answered, and EOF would wait the whole grace for it. Incoming lines are also queued for the SDK.
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
		h := held{line: append([]byte(nil), (*buf)[:i+1]...)}
		if json.Unmarshal((*buf)[:i], &m) == nil {
			switch {
			case len(m.ID) > 0 && string(m.ID) != "null":
				if incoming && m.Method != "" {
					t.pending[canonID(m.ID)] = true
					h.id, h.call, h.ordered = canonID(m.ID), true, true
				} else if !incoming && m.Method == "" {
					delete(t.pending, canonID(m.ID))
					delete(t.running, canonID(m.ID))
				}
			case incoming && m.Method == "notifications/cancelled":
				id := canonID(m.Params.RequestID)
				delete(t.pending, id)
				delete(t.running, id) // it may never be answered, and the next call must not wait for that
				t.queue = slices.DeleteFunc(t.queue, func(q held) bool { return q.call && q.id == id })
				h.cancel = true
			case incoming && m.Method != "":
				h.ordered = true // a notification keeps its place among the calls
			}
		}
		if incoming {
			t.queue = append(t.queue, h)
		}
		*buf = (*buf)[i+1:]
	}
	select {
	case t.changed <- struct{}{}:
	default:
	}
}

// release takes the lines that may go to the SDK now: every cancellation, and the queue in order up to the first call
// that has to wait for the one running.
func (t *tracker) release() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []byte
	var rest []held
	blocked := false
	for _, h := range t.queue {
		switch {
		case !h.ordered:
		case blocked:
			rest = append(rest, h)
			continue
		case h.call && len(t.running) > 0:
			blocked = true
			rest = append(rest, h)
			continue
		case h.call:
			t.running[h.id] = true
		}
		out = append(out, h.line...)
	}
	t.queue = rest
	return out
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

// trackedReader hands the SDK what release lets through. A goroutine reads stdin meanwhile, so a cancellation is seen
// while the request before it is still being handled.
type trackedReader struct {
	in  io.ReadCloser
	t   *tracker
	buf []byte
}

func (r *trackedReader) pump() {
	b := make([]byte, 32*1024)
	for {
		n, err := r.in.Read(b)
		if n > 0 {
			r.t.feed(b[:n], true)
		}
		if err != nil {
			r.t.mu.Lock()
			last := len(r.t.inLine) > 0
			r.t.mu.Unlock()
			if last {
				r.t.feed([]byte("\n"), true) // the last line may come without its newline
			}
			r.t.mu.Lock()
			r.t.eof = true
			if err != io.EOF {
				r.t.readErr = err
			}
			r.t.mu.Unlock()
			select {
			case r.t.changed <- struct{}{}:
			default:
			}
			return
		}
	}
}

func (r *trackedReader) Read(p []byte) (int, error) {
	var grace <-chan time.Time // runs from the moment stdin is closed, so held back calls cannot wait for ever
	for len(r.buf) == 0 {
		r.buf = r.t.release()
		if len(r.buf) > 0 {
			break
		}
		r.t.mu.Lock()
		eof, empty, err := r.t.eof, len(r.t.queue) == 0, r.t.readErr
		r.t.mu.Unlock()
		if eof && grace == nil {
			grace = time.After(eofGrace)
		}
		if eof && empty {
			if err != nil {
				return 0, err
			}
			r.t.drain()
			return 0, io.EOF
		}
		select {
		case <-r.t.changed:
		case <-grace:
			return 0, io.EOF
		}
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func (r *trackedReader) Close() error { return r.in.Close() }

type trackedWriter struct {
	io.WriteCloser
	t *tracker
}

func (w *trackedWriter) Write(p []byte) (int, error) {
	n, err := w.WriteCloser.Write(p)
	w.t.feed(p[:n], false)
	return n, err
}
