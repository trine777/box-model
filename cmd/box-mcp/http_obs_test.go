package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/windborneos/box-model/box/obs"
)

// R0.23 F4 fix: HTTP routes (/blob/upload, /items/<id>/blob) used to
// bypass the MemObserver entirely. After wiring withObs middleware,
// box_observability now reflects HTTP traffic as well as MCP-tool traffic.

func TestWithObs_CountsSuccess(t *testing.T) {
	o := obs.NewMemObserver(io.Discard, slog.LevelInfo)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	h := withObs(o, "test.route", inner)
	srv := httptest.NewServer(h)
	defer srv.Close()

	for i := 0; i < 3; i++ {
		resp, err := http.Get(srv.URL)
		if err != nil {
			t.Fatalf("get %d: %v", i, err)
		}
		resp.Body.Close()
	}

	snap := o.Snapshot().Summarize()
	if got := snap.Counters["test.route.attempt|method=GET"]; got != 3 {
		t.Errorf("attempt counter: got %d want 3 (keys=%v)", got, keysOfM(snap.Counters))
	}
	if got := snap.Counters["test.route.success|method=GET"]; got != 3 {
		t.Errorf("success counter: got %d want 3", got)
	}
	if errStats, ok := snap.Counters["test.route.error|method=GET,status=500"]; ok {
		t.Errorf("unexpected error counter: %d", errStats)
	}
	o2 := snap.Observed["test.route.duration_ms|method=GET"]
	if o2.Count != 3 {
		t.Errorf("duration count: got %d want 3", o2.Count)
	}
}

func TestWithObs_CountsErrorStatus(t *testing.T) {
	o := obs.NewMemObserver(io.Discard, slog.LevelInfo)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadRequest)
	})
	h := withObs(o, "test.bad", inner)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, _ := http.Get(srv.URL)
	resp.Body.Close()

	snap := o.Snapshot().Summarize()
	if snap.Counters["test.bad.attempt|method=GET"] != 1 {
		t.Errorf("attempt counter missing")
	}
	if snap.Counters["test.bad.success|method=GET"] != 0 {
		t.Errorf("success must NOT increment for 400")
	}
	if got := snap.Counters["test.bad.error|method=GET,status=400"]; got != 1 {
		t.Errorf("error counter: got %d want 1 (keys=%v)", got, keysOfM(snap.Counters))
	}
}

func TestWithObs_NilObserverIsPassthrough(t *testing.T) {
	// Calling withObs with a nil observer must return the next handler
	// unchanged — used by tests that don't bother wiring obs.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
	})
	h := withObs(nil, "x", inner)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, []byte("hello")) {
		t.Errorf("nil-obs passthrough broke body: got %q", body)
	}
}

func keysOfM[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
