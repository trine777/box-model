package obs_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/windborneos/box-model/box/obs"
)

// TestNoopObserverZeroAlloc asserts that the NoopObserver does zero
// heap allocations on the hot path. nil tags are passed (the harness
// must not allocate a map for those), and the Timer stop func must
// itself be a package-level value (not a new closure each call).
func TestNoopObserverZeroAlloc(t *testing.T) {
	var no obs.NoopObserver
	allocs := testing.AllocsPerRun(1000, func() {
		no.Inc("x.y", nil)
		stop := no.Timer("x.y", nil)
		stop()
		no.Observe("x.y", 1.0, nil)
		no.LogInfo("msg")
	})
	if allocs > 0 {
		t.Fatalf("NoopObserver should be zero-alloc, got %.1f", allocs)
	}
}

// TestMemObserverCounter exercises high contention on a single counter
// to make sure increments are atomic under load.
func TestMemObserverCounter(t *testing.T) {
	o := obs.NewMemObserver(io.Discard, slog.LevelInfo)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				o.Inc("x.y", nil)
			}
		}()
	}
	wg.Wait()
	snap := o.Snapshot()
	if got := snap.Counters["x.y"]; got != 10000 {
		t.Fatalf("counter x.y = %d, want 10000", got)
	}
}

// TestMemObserverTimer asserts that each stop-call yields one sample.
func TestMemObserverTimer(t *testing.T) {
	o := obs.NewMemObserver(io.Discard, slog.LevelInfo)
	const N = 50
	for i := 0; i < N; i++ {
		stop := o.Timer("x.y", nil)
		stop()
	}
	snap := o.Snapshot()
	if got := len(snap.Timers["x.y"]); got != N {
		t.Fatalf("timer x.y samples = %d, want %d", got, N)
	}
}

// TestMemObserverTagsKey asserts that two calls with the same logical
// tag map but different iteration order end up at the same counter
// (i.e., tags are canonicalized by sorted key).
func TestMemObserverTagsKey(t *testing.T) {
	o := obs.NewMemObserver(io.Discard, slog.LevelInfo)
	o.Inc("x", map[string]string{"a": "1", "b": "2"})
	o.Inc("x", map[string]string{"b": "2", "a": "1"})
	snap := o.Snapshot()
	// We don't pin the exact key format here — assert exactly one tagged
	// key exists with value 2.
	hits := 0
	for k, v := range snap.Counters {
		if strings.HasPrefix(k, "x|") && v == 2 {
			hits++
		}
	}
	if hits != 1 {
		t.Fatalf("expected exactly one tagged counter with value 2, got counters=%v", snap.Counters)
	}
}

// TestMemObserverLogJSON asserts that each emitted log line is a single
// JSON object with the expected base fields, and that the kv pairs land
// as top-level attributes.
func TestMemObserverLogJSON(t *testing.T) {
	var buf bytes.Buffer
	o := obs.NewMemObserver(&buf, slog.LevelInfo)
	o.LogInfo("hello", "op", "Store", "box_id", "box_x")
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d: %q", len(lines), buf.String())
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatalf("not JSON: %v line=%s", err, lines[0])
	}
	for _, k := range []string{"time", "level", "msg", "op"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("missing key %q in %v", k, m)
		}
	}
	if m["msg"] != "hello" {
		t.Fatalf("msg=%v", m["msg"])
	}
	if m["op"] != "Store" {
		t.Fatalf("op=%v", m["op"])
	}
}

// TestMemObserverLogLevel asserts that records below the minimum level
// are dropped at the slog handler boundary.
func TestMemObserverLogLevel(t *testing.T) {
	var buf bytes.Buffer
	o := obs.NewMemObserver(&buf, slog.LevelWarn)
	o.LogInfo("info-msg")
	o.LogWarn("warn-msg")
	out := buf.String()
	if strings.Contains(out, "info-msg") {
		t.Fatalf("info-msg should be filtered, got: %s", out)
	}
	if !strings.Contains(out, "warn-msg") {
		t.Fatalf("warn-msg should be present, got: %s", out)
	}
}

// TestMemObserverReset asserts that Reset clears the three accumulators.
func TestMemObserverReset(t *testing.T) {
	o := obs.NewMemObserver(io.Discard, slog.LevelInfo)
	o.Inc("a", nil)
	stop := o.Timer("a", nil)
	stop()
	o.Observe("a", 1.0, nil)
	o.Reset()
	snap := o.Snapshot()
	if len(snap.Counters) != 0 {
		t.Fatalf("counters not empty after reset: %v", snap.Counters)
	}
	if len(snap.Timers) != 0 {
		t.Fatalf("timers not empty after reset: %v", snap.Timers)
	}
	if len(snap.Observed) != 0 {
		t.Fatalf("observed not empty after reset: %v", snap.Observed)
	}
}
