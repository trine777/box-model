package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// fixtureSnapshot is a synthetic obs-metrics content blob matching spec a5,
// used to drive renderDashboardHTML without touching the live observation
// plane. This is the business plane (:7777) where development tasks live.
const fixtureSnapshot = `{
  "instance": "100.83.33.126:7777",
  "host": "100.83.33.126",
  "taken_at": "20260529T083205Z",
  "capacity": {"disk_used": 213608263680, "disk_total": 494384795648,
    "box_home_bytes": 19034112, "blob_count": 5, "blob_bytes": 476,
    "proc_rss_mb": 222.3, "proc_cpu": 0.2, "proc_uptime_s": 276090},
  "tasks": {"total": 30, "by_status": {"✓": 10, "→": 18, "✗": 2}, "completion_rate": 0.33, "stuck": 1,
    "durations_ms": [1000, 3000]},
  "perf": {"ops": [
    {"op": "box.summary", "calls": 5, "errors": 0, "err_pct": 0, "err_types": {}, "avg_ms": 0, "p95_ms": 0},
    {"op": "box.get_by_key", "calls": 4, "errors": 2, "err_pct": 50, "err_types": {"notfound": 2}, "avg_ms": 0, "p95_ms": 0},
    {"op": "item.store", "calls": 3, "errors": 0, "err_pct": 0, "err_types": {}, "avg_ms": 6, "p95_ms": 8}
  ]},
  "business": {"box_count": 2, "item_total": 11, "per_box": [
    {"key": "obs-fleet", "items": 8, "latest_stored_at": "2026-05-29T07:57:37Z", "uses": 0},
    {"key": "obs-metrics", "items": 3, "latest_stored_at": "2026-05-29T08:32:05Z", "uses": 2}
  ]}
}`

// fixtureObsPlane is the observation plane (:7788), which has ZERO development
// tasks. Pre-R15.1 this rendered a misleading "task total 0 完整度 0%" card.
const fixtureObsPlane = `{
  "instance": "100.83.33.126:7788",
  "host": "100.83.33.126",
  "taken_at": "20260529T083210Z",
  "capacity": {"disk_used": 213608263680, "disk_total": 494384795648,
    "box_home_bytes": 1000000, "blob_count": 0, "blob_bytes": 0,
    "proc_rss_mb": 30.1, "proc_cpu": 0.1, "proc_uptime_s": 1000},
  "tasks": {"total": 0, "by_status": {}, "completion_rate": 0, "stuck": 0},
  "perf": {"ops": []},
  "business": {"box_count": 1, "item_total": 1, "per_box": [
    {"key": "obs-metrics", "items": 1, "latest_stored_at": "2026-05-29T08:32:10Z", "uses": 0}
  ]}
}`

func decodeFixture(t *testing.T) snapshot {
	t.Helper()
	var s snapshot
	if err := json.Unmarshal([]byte(fixtureSnapshot), &s); err != nil {
		t.Fatalf("fixture unmarshal: %v", err)
	}
	return s
}

func decodeObsPlane(t *testing.T) snapshot {
	t.Helper()
	var s snapshot
	if err := json.Unmarshal([]byte(fixtureObsPlane), &s); err != nil {
		t.Fatalf("obs-plane fixture unmarshal: %v", err)
	}
	return s
}

func TestRenderRealNumbers(t *testing.T) {
	s := decodeFixture(t)
	out := renderDashboardHTML([]snapshot{s})

	// disk % = 213608263680 / 494384795648 ≈ 43.2%
	for _, want := range []string{
		"100.83.33.126:7777", // instance
		"43.2%",              // disk %
		"box.get_by_key",     // op row
		"50%",                // op err% (high, flagged)
		"notfound",           // err_type
		"p95 ms",             // p95 column present
		"8.0",                // item.store p95_ms = 8
		"obs-metrics",        // per-box row (business table)
		"使用次数",               // per-box uses column
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard HTML missing %q", want)
		}
	}

	// high err% op must carry the bad class (red).
	if !strings.Contains(out, `class="bad">50%`) {
		t.Errorf("50%% err op should be flagged with bad class")
	}

	// ZERO five-element / 觉痕 symbols may appear anywhere in the output.
	for _, banned := range []string{"五元素", "觉痕", "风", "火", "土", "水", "空", "脉搏", "●", "○"} {
		if strings.Contains(out, banned) {
			t.Errorf("dashboard HTML must not contain symbolic abstraction %q", banned)
		}
	}
}

// TestSystemTaskSummary checks the R15.1 system-level task rollup: task is no
// longer a per-instance card; it is one aggregate summary at the top of the
// page, summed over ALL instances' latest snapshots.
func TestSystemTaskSummary(t *testing.T) {
	biz := decodeFixture(t)    // :7777 total=30, ✓:10 →:18 ✗:2, durations [1000,3000]
	obs := decodeObsPlane(t)   // :7788 total=0
	out := renderDashboardHTML([]snapshot{biz, obs})

	// aggregate: total=30, ✓=10 -> completion 10/30 = 33%
	for _, want := range []string{
		"系统 task",          // system-level summary block header
		"全舰队聚合",            // aggregate label
		`total <b>30</b>`, // Σ total = 30 + 0
		`完整度 <b>33%</b>`,  // 10/30 = 33%
		"✓:10",            // merged by_status
		"→:18",
		"✗:2",
		"avg duration",       // avg duration label
		`avg duration <b>2s</b>`, // (1000+3000)/2 ms = 2000ms = 2s
	} {
		if !strings.Contains(out, want) {
			t.Errorf("system task summary missing %q", want)
		}
	}

	// stuck total = 1, flagged bad.
	if !strings.Contains(out, `卡住 <b class="bad">1</b>`) {
		t.Errorf("aggregate stuck should be 1 and bad-flagged")
	}

	// The instance cards must NOT carry per-instance task / 完整度 sections
	// anymore. The only "task" / "完整度" occurrences must be the single
	// system-level block at the top.
	if c := strings.Count(out, "完整度"); c != 1 {
		t.Errorf("完整度 should appear exactly once (system summary), got %d", c)
	}
	// "task 执行" was the old per-instance section header; it must be gone.
	if strings.Contains(out, "task 执行") {
		t.Errorf("per-instance 'task 执行' section must be removed")
	}
}

func TestAggregateTasksZeroTotal(t *testing.T) {
	// All-zero fleet: completion_rate must be 0 (no divide-by-zero).
	obs := decodeObsPlane(t)
	a := aggregateTasks([]snapshot{obs})
	if a.Total != 0 || a.CompletionRate != 0 || a.AvgDurationS != 0 {
		t.Errorf("zero-task fleet should give total=0 rate=0 avg=0, got %+v", a)
	}
}

func TestRenderAbsentMemberDown(t *testing.T) {
	// Only :7777 reports; :7788 is an expected member and must show down.
	s := decodeFixture(t)
	out := renderDashboardHTML([]snapshot{s})
	if !strings.Contains(out, "100.83.33.126:7788") || !strings.Contains(out, "down") {
		t.Errorf("absent expected member :7788 should render as down")
	}
}

func TestRenderEmptyState(t *testing.T) {
	out := renderDashboardHTML(nil)
	if !strings.Contains(out, "暂无快照") {
		t.Errorf("empty state missing placeholder")
	}
	// Even empty, expected members are flagged down for visibility.
	if !strings.Contains(out, "100.83.33.126:7777") {
		t.Errorf("empty state should still list expected members as down")
	}
}
