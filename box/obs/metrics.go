package obs

import (
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// Snapshot is a read-only point-in-time view returned by MemObserver.Snapshot.
// All maps are freshly allocated copies — callers may mutate them without
// affecting the live MemObserver state.
type Snapshot struct {
	Counters map[string]int64
	Timers   map[string][]time.Duration
	Observed map[string][]float64
}

// MemObserver implements Observer with in-memory accumulators and writes
// structured JSON logs via log/slog. It is safe for concurrent use under a
// single internal mutex.
//
// Storage layout:
//   - counters: map[name|k1=v1,k2=v2] -> int64 cumulative
//   - timers:   map[name|...] -> []Duration raw samples
//   - observed: map[name|...] -> []float64 raw samples
//
// Tag-key canonicalization: tags are encoded as a comma-separated list of
// k=v pairs in sorted-key order, joined to the metric name by '|'. Calls with
// identical logical tag maps but different iteration orders therefore land at
// the same accumulator key.
type MemObserver struct {
	mu       sync.Mutex
	counters map[string]int64
	timers   map[string][]time.Duration
	observed map[string][]float64
	logger   *slog.Logger
	clock    func() time.Time
}

// NewMemObserver builds a MemObserver. If logSink is nil, log records are
// discarded (slog wired to io.Discard). minLevel is the minimum slog level
// that will be emitted; lower-level records are dropped at the handler.
func NewMemObserver(logSink io.Writer, minLevel slog.Level) *MemObserver {
	if logSink == nil {
		logSink = io.Discard
	}
	handler := slog.NewJSONHandler(logSink, &slog.HandlerOptions{Level: minLevel})
	return &MemObserver{
		counters: map[string]int64{},
		timers:   map[string][]time.Duration{},
		observed: map[string][]float64{},
		logger:   slog.New(handler),
		clock:    time.Now,
	}
}

// keyFor builds the canonical accumulator key name|sortedTags. Empty tags
// returns the bare name; a nil tags map produces no allocation beyond the
// returned string (cheap for the no-tag hot path).
func keyFor(name string, tags map[string]string) string {
	if len(tags) == 0 {
		return name
	}
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.Grow(len(name) + 1 + len(keys)*8)
	b.WriteString(name)
	b.WriteByte('|')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(tags[k])
	}
	return b.String()
}

// Inc atomically (under mu) bumps the counter for name+tags by one.
func (o *MemObserver) Inc(name string, tags map[string]string) {
	k := keyFor(name, tags)
	o.mu.Lock()
	o.counters[k]++
	o.mu.Unlock()
}

// Timer starts a duration sample and returns a stop func that, when called,
// appends the elapsed wall-clock duration to the timer series.
func (o *MemObserver) Timer(name string, tags map[string]string) func() {
	start := o.clock()
	k := keyFor(name, tags)
	return func() {
		d := o.clock().Sub(start)
		o.mu.Lock()
		o.timers[k] = append(o.timers[k], d)
		o.mu.Unlock()
	}
}

// Observe appends a single floating-point sample to the named series.
func (o *MemObserver) Observe(name string, value float64, tags map[string]string) {
	k := keyFor(name, tags)
	o.mu.Lock()
	o.observed[k] = append(o.observed[k], value)
	o.mu.Unlock()
}

// TimerStats summarises a slice of duration / float samples into the four
// shapes an external observer typically wants: count + sum + avg + min + max.
// Raw samples (Snapshot.Timers / Snapshot.Observed) carry full precision but
// are too verbose for MCP responses; SnapshotSummary uses TimerStats instead.
type TimerStats struct {
	Count int     `json:"count"`
	SumMs float64 `json:"sum_ms"`
	AvgMs float64 `json:"avg_ms"`
	MinMs float64 `json:"min_ms"`
	MaxMs float64 `json:"max_ms"`
}

// SnapshotSummary is the wire-format counterpart to Snapshot. It compresses
// the raw sample arrays into TimerStats so MCP responses stay small even
// with thousands of recorded calls.
type SnapshotSummary struct {
	Counters map[string]int64      `json:"counters"`
	Timers   map[string]TimerStats `json:"timers"`
	Observed map[string]TimerStats `json:"observed"`
	TakenAt  time.Time             `json:"taken_at"`
}

// Summarize compresses a Snapshot into a SnapshotSummary. Empty input maps
// become empty (non-nil) output maps so JSON encodes them as `{}` not
// `null` — matters for clients with strict deserialisers.
func (s Snapshot) Summarize() SnapshotSummary {
	out := SnapshotSummary{
		Counters: map[string]int64{},
		Timers:   map[string]TimerStats{},
		Observed: map[string]TimerStats{},
		TakenAt:  time.Now().UTC(),
	}
	for k, v := range s.Counters {
		out.Counters[k] = v
	}
	for k, v := range s.Timers {
		out.Timers[k] = statsFromDurations(v)
	}
	for k, v := range s.Observed {
		out.Observed[k] = statsFromFloats(v)
	}
	return out
}

// FilterPrefix returns a copy of summary with only entries whose key starts
// with the supplied prefix (matched against the bare metric name, before
// the `|` tag separator). An empty prefix returns a defensive copy.
func (s SnapshotSummary) FilterPrefix(prefix string) SnapshotSummary {
	out := SnapshotSummary{
		Counters: map[string]int64{},
		Timers:   map[string]TimerStats{},
		Observed: map[string]TimerStats{},
		TakenAt:  s.TakenAt,
	}
	matches := func(key string) bool {
		if prefix == "" {
			return true
		}
		// Strip any tag suffix so "box.create" matches "box.create|err=…".
		name := key
		if i := strings.Index(key, "|"); i >= 0 {
			name = key[:i]
		}
		return strings.HasPrefix(name, prefix)
	}
	for k, v := range s.Counters {
		if matches(k) {
			out.Counters[k] = v
		}
	}
	for k, v := range s.Timers {
		if matches(k) {
			out.Timers[k] = v
		}
	}
	for k, v := range s.Observed {
		if matches(k) {
			out.Observed[k] = v
		}
	}
	return out
}

func statsFromDurations(samples []time.Duration) TimerStats {
	if len(samples) == 0 {
		return TimerStats{}
	}
	toMs := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }
	min := toMs(samples[0])
	max := min
	sum := 0.0
	for _, d := range samples {
		v := toMs(d)
		sum += v
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	return TimerStats{
		Count: len(samples),
		SumMs: sum,
		AvgMs: sum / float64(len(samples)),
		MinMs: min,
		MaxMs: max,
	}
}

func statsFromFloats(samples []float64) TimerStats {
	if len(samples) == 0 {
		return TimerStats{}
	}
	min := samples[0]
	max := min
	sum := 0.0
	for _, v := range samples {
		sum += v
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	return TimerStats{
		Count: len(samples),
		SumMs: sum,
		AvgMs: sum / float64(len(samples)),
		MinMs: min,
		MaxMs: max,
	}
}

// Snapshot returns a copy of the current accumulators. Subsequent
// mutations on the live MemObserver do not affect the returned maps.
func (o *MemObserver) Snapshot() Snapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	c := make(map[string]int64, len(o.counters))
	for k, v := range o.counters {
		c[k] = v
	}
	t := make(map[string][]time.Duration, len(o.timers))
	for k, v := range o.timers {
		cp := make([]time.Duration, len(v))
		copy(cp, v)
		t[k] = cp
	}
	obsv := make(map[string][]float64, len(o.observed))
	for k, v := range o.observed {
		cp := make([]float64, len(v))
		copy(cp, v)
		obsv[k] = cp
	}
	return Snapshot{Counters: c, Timers: t, Observed: obsv}
}

// Reset clears all three accumulators. The slog logger and clock are kept.
func (o *MemObserver) Reset() {
	o.mu.Lock()
	o.counters = map[string]int64{}
	o.timers = map[string][]time.Duration{}
	o.observed = map[string][]float64{}
	o.mu.Unlock()
}

// compile-time assertion: MemObserver satisfies Observer.
var _ Observer = (*MemObserver)(nil)
