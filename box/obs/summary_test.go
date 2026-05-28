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
