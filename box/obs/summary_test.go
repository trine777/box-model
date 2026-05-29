package obs

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestSnapshotSummarize_Empty(t *testing.T) {
	got := Snapshot{}.Summarize()
	if got.Counters == nil || got.Timers == nil || got.Observed == nil {
		t.Fatalf("empty Snapshot should still produce non-nil maps (got %+v)", got)
	}
	if got.TakenAt.IsZero() {
		t.Errorf("TakenAt should be set even for empty summary")
	}
}

func TestSnapshotSummarize_RealAccumulators(t *testing.T) {
	o := NewMemObserver(io.Discard, slog.LevelInfo)
	tags := map[string]string{}
	o.Inc("box.create.success", tags)
	o.Inc("box.create.success", tags)
	o.Inc("item.store.attempt", tags)
	// timer samples (ms-equivalent durations)
	o.Observe("box.create.duration_ms", 1.0, tags)
	o.Observe("box.create.duration_ms", 5.0, tags)
	o.Observe("box.create.duration_ms", 9.0, tags)

	snap := o.Snapshot().Summarize()
	if snap.Counters["box.create.success"] != 2 {
		t.Errorf("counter: got %d want 2", snap.Counters["box.create.success"])
	}
	if snap.Counters["item.store.attempt"] != 1 {
		t.Errorf("counter: got %d want 1", snap.Counters["item.store.attempt"])
	}
	o2 := snap.Observed["box.create.duration_ms"]
	if o2.Count != 3 {
		t.Errorf("observed count: got %d want 3", o2.Count)
	}
	if o2.MinMs != 1.0 || o2.MaxMs != 9.0 {
		t.Errorf("observed min/max: got %v/%v want 1/9", o2.MinMs, o2.MaxMs)
	}
	if o2.SumMs != 15.0 {
		t.Errorf("observed sum: got %v want 15", o2.SumMs)
	}
	if o2.AvgMs != 5.0 {
		t.Errorf("observed avg: got %v want 5", o2.AvgMs)
	}
}

func TestStatsFromDurations_MillisecondConversion(t *testing.T) {
	samples := []time.Duration{
		1 * time.Millisecond,
		2 * time.Millisecond,
		3 * time.Millisecond,
	}
	got := statsFromDurations(samples)
	if got.Count != 3 || got.SumMs != 6.0 || got.AvgMs != 2.0 || got.MinMs != 1.0 || got.MaxMs != 3.0 {
		t.Errorf("durations summary off: %+v", got)
	}
}

func TestStatsFromFloats_P95(t *testing.T) {
	// Samples 1..100; nearest-rank p95 of sorted [1..100] is index
	// round(0.95*99)=round(94.05)=94 -> value 95.
	samples := make([]float64, 0, 100)
	for i := 1; i <= 100; i++ {
		samples = append(samples, float64(i))
	}
	got := statsFromFloats(samples)
	if got.P95Ms != 95.0 {
		t.Errorf("p95 of 1..100: got %v want 95", got.P95Ms)
	}
	// statsFromFloats must not mutate caller's slice ordering.
	if samples[0] != 1.0 || samples[99] != 100.0 {
		t.Errorf("statsFromFloats mutated input slice: %v..%v", samples[0], samples[99])
	}

	// Single sample: p95 == that sample.
	if s := statsFromFloats([]float64{7.0}); s.P95Ms != 7.0 {
		t.Errorf("single-sample p95: got %v want 7", s.P95Ms)
	}
	// Empty: p95 == 0.
	if s := statsFromFloats(nil); s.P95Ms != 0 {
		t.Errorf("empty p95: got %v want 0", s.P95Ms)
	}
	// Unsorted input still yields the correct percentile.
	if s := statsFromFloats([]float64{50, 1, 99, 2, 100}); s.P95Ms != 100.0 {
		t.Errorf("unsorted p95: got %v want 100", s.P95Ms)
	}
}

func TestStatsFromDurations_P95(t *testing.T) {
	samples := make([]time.Duration, 0, 100)
	for i := 1; i <= 100; i++ {
		samples = append(samples, time.Duration(i)*time.Millisecond)
	}
	got := statsFromDurations(samples)
	if got.P95Ms != 95.0 {
		t.Errorf("durations p95 of 1..100ms: got %v want 95", got.P95Ms)
	}
	if s := statsFromDurations(nil); s.P95Ms != 0 {
		t.Errorf("empty durations p95: got %v want 0", s.P95Ms)
	}
}

func TestSummary_FilterPrefix(t *testing.T) {
	o := NewMemObserver(io.Discard, slog.LevelInfo)
	o.Inc("box.create.success", nil)
	o.Inc("item.store.success", nil)
	o.Inc("box.delete.success", map[string]string{"err": "x"}) // adds tag suffix `|err=x`

	full := o.Snapshot().Summarize()
	if _, ok := full.Counters["item.store.success"]; !ok {
		t.Fatalf("setup: missing item.store.success")
	}

	box := full.FilterPrefix("box.")
	if _, ok := box.Counters["item.store.success"]; ok {
		t.Errorf("prefix=box. should not include item.store.success")
	}
	if _, ok := box.Counters["box.create.success"]; !ok {
		t.Errorf("prefix=box. should include box.create.success")
	}
	// tag-suffixed keys keep their tags but should still match by name prefix
	want := "box.delete.success|err=x"
	if _, ok := box.Counters[want]; !ok {
		t.Errorf("prefix=box. should match tag-suffixed key %q; got %v", want, box.Counters)
	}
}
