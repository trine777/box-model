package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/windborneos/box-model/box"
	"github.com/windborneos/box-model/box/obs"
)

// TestObservabilityHandler — counters increment after real Service calls;
// snapshot reflects the bumps; prefix filter works.
func TestObservabilityHandler(t *testing.T) {
	ctx := context.Background()
	st := box.NewMemoryStore()
	o := obs.NewMemObserver(io.Discard, slog.LevelInfo)
	svc := box.NewService(st, box.WithObserver(o))

	// Drive the service so counters move.
	if _, err := svc.CreateBox(ctx, box.CreateBoxRequest{Key: "obs-test", OwnerID: "alice"}); err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	if _, err := svc.GetBoxByKey(ctx, "alice", "obs-test"); err != nil {
		t.Fatalf("GetBoxByKey: %v", err)
	}

	full := svc.ObservabilitySnapshot(ctx, "")
	if len(full.Counters) == 0 {
		t.Fatalf("expected counters to be populated, got empty")
	}
	// box.create.* and box.get_by_key.* are the metric families the service emits.
	if !hasPrefix(full.Counters, "box.create.") {
		t.Errorf("expected counter with prefix box.create.*, got keys: %v", keysOf(full.Counters))
	}
	if !hasPrefix(full.Counters, "box.get_by_key.") {
		t.Errorf("expected counter with prefix box.get_by_key.*, got keys: %v", keysOf(full.Counters))
	}

	// Prefix filter: only box.create.* survives.
	create := svc.ObservabilitySnapshot(ctx, "box.create")
	if !hasPrefix(create.Counters, "box.create.") {
		t.Errorf("filter prefix=box.create lost the matching family")
	}
	for k := range create.Counters {
		if !startsWithDot(k, "box.create") {
			t.Errorf("filter leak: key %q not in box.create family", k)
		}
	}
	// TakenAt must be populated (clients use it to ttl-cache)
	if create.TakenAt.IsZero() {
		t.Errorf("TakenAt should be set")
	}
}

// TestObservabilityHandler_NoopObserver returns empty (non-nil) maps so the
// caller can range over them without nil-checks.
func TestObservabilityHandler_NoopObserver(t *testing.T) {
	ctx := context.Background()
	st := box.NewMemoryStore()
	svc := box.NewService(st) // no WithObserver → NoopObserver default
	got := svc.ObservabilitySnapshot(ctx, "")
	if got.Counters == nil || got.Timers == nil || got.Observed == nil {
		t.Errorf("NoopObserver path must still return non-nil maps: %+v", got)
	}
	if len(got.Counters) != 0 {
		t.Errorf("NoopObserver should produce empty counters, got %v", got.Counters)
	}
}

// helpers ------------------------------------------------------------------

func hasPrefix[V any](m map[string]V, prefix string) bool {
	for k := range m {
		if startsWithDot(k, prefix) {
			return true
		}
	}
	return false
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// startsWithDot is HasPrefix but tolerant of tag suffixes ("name|tag=v").
func startsWithDot(key, prefix string) bool {
	if len(key) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		if key[i] != prefix[i] {
			return false
		}
	}
	return true
}
