package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCheckTokenRefusesEmptyAndShortTokens(t *testing.T) {
	for token, ok := range map[string]bool{"": false, "short": false, strings.Repeat("a", 31): false, strings.Repeat("a", 32): true} {
		if err := checkToken(token); (err == nil) != ok {
			t.Errorf("%d characters: err = %v, ok = %v", len(token), err, ok)
		}
	}
}

func TestBoundedRefusesABodyOverTheCapAndLetsASmallOneThrough(t *testing.T) {
	old := maxRequestBody
	maxRequestBody = 1000
	t.Cleanup(func() { maxRequestBody = old })
	srv := httptest.NewServer(bounded(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, "too big", http.StatusRequestEntityTooLarge)
		}
	})))
	defer srv.Close()
	for size, want := range map[int]int{999: 200, 5000: http.StatusRequestEntityTooLarge} {
		res, err := http.Post(srv.URL, "text/plain", bytes.NewReader(make([]byte, size)))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != want {
			t.Errorf("%d bytes: %d, want %d", size, res.StatusCode, want)
		}
	}
}

func TestBoundedWriteDeadlineCountsFromTheEndOfTheBodyNotTheStartOfTheUpload(t *testing.T) {
	oldRead, oldWrite := bodyReadTimeout, responseWriteTimeout
	bodyReadTimeout, responseWriteTimeout = time.Hour, 300*time.Millisecond
	t.Cleanup(func() { bodyReadTimeout, responseWriteTimeout = oldRead, oldWrite })
	srv := httptest.NewServer(bounded(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		if r.URL.Query().Get("slow") != "" {
			time.Sleep(800 * time.Millisecond)
		} else {
			time.Sleep(100 * time.Millisecond)
		}
		_, _ = io.WriteString(w, "done")
	})))
	defer srv.Close()
	// had the deadline been set at the start with the upload's hour, the slow answer would also have gone through
	for q, ok := range map[string]bool{"": true, "?slow=1": false} {
		res, err := http.Post(srv.URL+q, "text/plain", strings.NewReader("x"))
		if ok != (err == nil) {
			t.Errorf("%q: err = %v, want ok = %v", q, err, ok)
		}
		if err == nil {
			res.Body.Close()
		}
	}
}

func TestBoundedCutsAStalledUploadButNotAnAnswerThatTakesLonger(t *testing.T) {
	old := bodyReadTimeout
	bodyReadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { bodyReadTimeout = old })
	srv := httptest.NewServer(bounded(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "upload stalled", http.StatusRequestTimeout)
			return
		}
		// the work outlasts the upload's deadline: it must not cancel the request
		time.Sleep(900 * time.Millisecond)
		if r.Context().Err() != nil {
			t.Errorf("the request was cancelled while its handler worked: %v", r.Context().Err())
		}
		_, _ = io.WriteString(w, "done")
	})))
	defer srv.Close()

	res, err := http.Post(srv.URL, "text/plain", strings.NewReader("small"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(b) != "done" {
		t.Errorf("a slow answer: %d %q", res.StatusCode, b)
	}

	// a sender that promises a body and stops: the read ends at the deadline instead of holding the connection
	c, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = io.WriteString(c, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\nabc")
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	got, _ := io.ReadAll(c)
	if !strings.Contains(string(got), "408") {
		t.Errorf("a stalled upload was not cut: %q", got)
	}
}

func TestEnvNumbersFailOnAnUnparsableValueAndNameTheVariable(t *testing.T) {
	t.Setenv("KEDALIO_CORE_BACKUP_EVERY", "daily")
	t.Setenv("KEDALIO_CORE_BACKUP_KEEP", "7")
	t.Setenv("KEDALIO_CORE_BACKUP_S3_KEEP", "many")
	var bad error
	if d := envDuration("BACKUP_EVERY", time.Hour, &bad); d != time.Hour {
		t.Errorf("duration = %v, want the default beside the error", d)
	}
	if n := envInt("BACKUP_KEEP", 3, &bad); n != 7 {
		t.Errorf("BACKUP_KEEP = %d, want 7 from the environment", n)
	}
	envInt("BACKUP_S3_KEEP", 3, &bad)
	if bad == nil || !strings.Contains(bad.Error(), "KEDALIO_CORE_BACKUP_EVERY") || !strings.Contains(bad.Error(), "KEDALIO_CORE_BACKUP_S3_KEEP") {
		t.Errorf("err = %v, want both variables named", bad)
	}
}
