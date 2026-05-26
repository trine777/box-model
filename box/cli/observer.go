package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/windborneos/box-model/box/obs"
)

// observerPaths bundles the resolved on-disk paths used by the observer.
// snapshotPath holds counters/timers/observed accumulators between invocations
// (so `box stats` can show cross-process totals).
type observerPaths struct {
	logPath      string
	snapshotPath string
}

// resolveObserverPaths derives log/snapshot file paths from environment.
// Order: explicit BOX_LOG_PATH > BOX_HOME-relative > $HOME/.box.
func (rc *rootContext) resolveObserverPaths() observerPaths {
	home := rc.env("BOX_HOME")
	if home == "" {
		h, _ := os.UserHomeDir()
		home = filepath.Join(h, ".box")
	}
	logPath := rc.env("BOX_LOG_PATH")
	if logPath == "" {
		logPath = filepath.Join(home, "_logs", "box.log.jsonl")
	}
	snap := filepath.Join(home, "_metrics", "snapshot.json")
	return observerPaths{logPath: logPath, snapshotPath: snap}
}

// resolveLogLevel maps BOX_LOG_LEVEL → slog.Level. Default: info.
func (rc *rootContext) resolveLogLevel() slog.Level {
	switch strings.ToLower(rc.env("BOX_LOG_LEVEL")) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// buildObserver constructs the per-invocation Observer. BOX_OBS_DISABLED=1
// returns NoopObserver; otherwise a MemObserver writing JSON to the log file.
// Failure to open the log file falls back to NoopObserver — observability
// must never crash the CLI.
//
// Returns (observer, openedLogFile-or-nil, snapshotPath). The caller is
// responsible for closing the log file (typically via defer at the end of Run).
func (rc *rootContext) buildObserver() (obs.Observer, *os.File, string) {
	if rc.env("BOX_OBS_DISABLED") == "1" {
		return obs.NoopObserver{}, nil, ""
	}
	paths := rc.resolveObserverPaths()
	if err := os.MkdirAll(filepath.Dir(paths.logPath), 0o755); err != nil {
		return obs.NoopObserver{}, nil, ""
	}
	f, err := os.OpenFile(paths.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return obs.NoopObserver{}, nil, ""
	}
	return obs.NewMemObserver(f, rc.resolveLogLevel()), f, paths.snapshotPath
}

// persistedSnapshot is the on-disk shape of a metrics snapshot. We keep
// counters as exact totals; timers/observed are flattened to count + sum so
// per-process samples don't blow up unboundedly across many invocations.
type persistedSnapshot struct {
	Counters map[string]int64       `json:"counters"`
	Timers   map[string]aggSamples  `json:"timers"`
	Observed map[string]aggSamples  `json:"observed"`
}

type aggSamples struct {
	Count int64   `json:"count"`
	Sum   float64 `json:"sum"`
}

// mergeAndPersist reads an existing snapshot.json (if any), folds in the live
// observer's current snapshot, and atomically writes the merged result back.
// Per-invocation samples are aggregated to (count,sum) so the file stays
// O(metric keys) rather than O(samples).
func mergeAndPersist(o *obs.MemObserver, path string) error {
	snap := o.Snapshot()
	prev := persistedSnapshot{
		Counters: map[string]int64{},
		Timers:   map[string]aggSamples{},
		Observed: map[string]aggSamples{},
	}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &prev)
		if prev.Counters == nil {
			prev.Counters = map[string]int64{}
		}
		if prev.Timers == nil {
			prev.Timers = map[string]aggSamples{}
		}
		if prev.Observed == nil {
			prev.Observed = map[string]aggSamples{}
		}
	}
	for k, v := range snap.Counters {
		prev.Counters[k] += v
	}
	for k, samples := range snap.Timers {
		cur := prev.Timers[k]
		cur.Count += int64(len(samples))
		for _, d := range samples {
			cur.Sum += float64(d.Milliseconds())
		}
		prev.Timers[k] = cur
	}
	for k, samples := range snap.Observed {
		cur := prev.Observed[k]
		cur.Count += int64(len(samples))
		for _, v := range samples {
			cur.Sum += v
		}
		prev.Observed[k] = cur
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(prev, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// loadSnapshot reads the persisted snapshot at path; returns an empty value
// (no error) if the file doesn't exist yet — a fresh BOX_HOME just has no
// metrics yet, not an error condition.
func loadSnapshot(path string) (persistedSnapshot, error) {
	out := persistedSnapshot{
		Counters: map[string]int64{},
		Timers:   map[string]aggSamples{},
		Observed: map[string]aggSamples{},
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return out, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, err
	}
	if out.Counters == nil {
		out.Counters = map[string]int64{}
	}
	if out.Timers == nil {
		out.Timers = map[string]aggSamples{}
	}
	if out.Observed == nil {
		out.Observed = map[string]aggSamples{}
	}
	return out, nil
}

// printSnapshot writes a human-readable rollup of the snapshot to w. Lines are
// sorted by metric name for stable output. `nameFilter`, when non-empty, keeps
// only entries whose canonical metric name (the bit before "|") begins with it.
func printSnapshot(w io.Writer, snap persistedSnapshot, nameFilter string) {
	fmt.Fprintln(w, "counters:")
	keys := sortedKeysInt64(snap.Counters)
	for _, k := range keys {
		name, _ := splitNameTags(k)
		if nameFilter != "" && !strings.HasPrefix(name, nameFilter) {
			continue
		}
		fmt.Fprintf(w, "  %s\t%d\n", k, snap.Counters[k])
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "timers (avg_ms / count):")
	tKeys := sortedKeysAgg(snap.Timers)
	for _, k := range tKeys {
		name, _ := splitNameTags(k)
		if nameFilter != "" && !strings.HasPrefix(name, nameFilter) {
			continue
		}
		ag := snap.Timers[k]
		avg := 0.0
		if ag.Count > 0 {
			avg = ag.Sum / float64(ag.Count)
		}
		fmt.Fprintf(w, "  %s\t%.2f / %d\n", k, avg, ag.Count)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "observed (avg / count):")
	oKeys := sortedKeysAgg(snap.Observed)
	for _, k := range oKeys {
		name, _ := splitNameTags(k)
		if nameFilter != "" && !strings.HasPrefix(name, nameFilter) {
			continue
		}
		ag := snap.Observed[k]
		avg := 0.0
		if ag.Count > 0 {
			avg = ag.Sum / float64(ag.Count)
		}
		fmt.Fprintf(w, "  %s\t%.2f / %d\n", k, avg, ag.Count)
	}
}

func sortedKeysInt64(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysAgg(m map[string]aggSamples) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// splitNameTags splits a "name|k=v,k=v" key into (name, tagsPart). When the
// key has no tags, tagsPart is "".
func splitNameTags(k string) (string, string) {
	if i := strings.IndexByte(k, '|'); i >= 0 {
		return k[:i], k[i+1:]
	}
	return k, ""
}

// tailLogFile reads up to tail lines from the JSONL log file, applies the
// optional level/op/since filters, and writes survivors to w in original order.
// We implement tail by buffering the last N matched lines.
func tailLogFile(w io.Writer, path string, tail int, level, op string, since time.Duration) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	lines := strings.Split(string(data), "\n")
	now := time.Now()
	want := make([]string, 0, tail)
	for _, ln := range lines {
		if ln == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(ln), &rec); err != nil {
			continue
		}
		if level != "" && !levelAtLeast(stringField(rec, "level"), level) {
			continue
		}
		if op != "" && stringField(rec, "op") != op {
			continue
		}
		if since > 0 {
			ts, _ := time.Parse(time.RFC3339Nano, stringField(rec, "time"))
			if !ts.IsZero() && now.Sub(ts) > since {
				continue
			}
		}
		want = append(want, ln)
	}
	if tail > 0 && len(want) > tail {
		want = want[len(want)-tail:]
	}
	for _, ln := range want {
		fmt.Fprintln(w, ln)
	}
	return nil
}

// levelRank maps slog level names (case-insensitive) to a numeric rank for
// >= comparisons. Unknown level strings rank 0 (treated as Debug-ish), which
// is permissive — better than silently dropping legitimate lines.
func levelRank(lvl string) int {
	switch strings.ToUpper(lvl) {
	case "DEBUG":
		return 0
	case "INFO":
		return 1
	case "WARN", "WARNING":
		return 2
	case "ERROR":
		return 3
	}
	return 0
}

// levelAtLeast returns true if recordLevel >= minLevel.
func levelAtLeast(recordLevel, minLevel string) bool {
	return levelRank(recordLevel) >= levelRank(minLevel)
}

func stringField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
