// Package obs provides the observability surface (counters, timers, observed
// distributions, structured logs) used by the Service and CLI layers.
//
// Two implementations are shipped:
//
//   - NoopObserver: zero-overhead default. All methods are no-ops; the Timer
//     stop callback is a single package-level closure so even nil tag maps
//     cost zero heap allocations.
//   - MemObserver: in-memory accumulator (counters/timers/observed samples)
//     plus a slog JSON handler that writes one structured log line per record.
//
// See docs/observability.md for the full SoR of metric names, tag keys, and
// log fields. Service and FileStore call this package via the Observer
// interface; nothing in this package depends back on the box package.
package obs

// Observer is the public observability surface used by Service and CLI.
// Implementations must be safe for concurrent use.
type Observer interface {
	// Inc increments the named counter by one. tags should be low cardinality
	// (see docs/observability.md §4).
	Inc(name string, tags map[string]string)
	// Timer starts a duration sample. The returned stop func must be called
	// exactly once (typically via defer); the elapsed wall clock duration is
	// appended to the timer series keyed by name+tags.
	Timer(name string, tags map[string]string) func()
	// Observe records a single floating-point sample (e.g. result_count, revision).
	Observe(name string, value float64, tags map[string]string)
	// LogInfo emits an INFO-level structured log line. kv must be an even
	// number of arguments: key (string), value (any), key, value, ...
	LogInfo(msg string, kv ...any)
	// LogWarn emits a WARN-level structured log line. Same kv convention.
	LogWarn(msg string, kv ...any)
	// LogError emits an ERROR-level structured log line. err.Error() is
	// recorded under the "err" key automatically.
	LogError(msg string, err error, kv ...any)
}

// noopStop is the single shared stop closure returned by NoopObserver.Timer.
// Returning a package-level variable (rather than constructing a closure each
// call) is what keeps NoopObserver allocation-free in TestNoopObserverZeroAlloc.
var noopStop = func() {}

// NoopObserver is the zero-overhead default. All methods are no-ops. It is
// safe to use the zero value directly.
type NoopObserver struct{}

func (NoopObserver) Inc(string, map[string]string)              {}
func (NoopObserver) Timer(string, map[string]string) func()     { return noopStop }
func (NoopObserver) Observe(string, float64, map[string]string) {}
func (NoopObserver) LogInfo(string, ...any)                     {}
func (NoopObserver) LogWarn(string, ...any)                     {}
func (NoopObserver) LogError(string, error, ...any)             {}

// compile-time assertion: NoopObserver satisfies Observer.
var _ Observer = NoopObserver{}
