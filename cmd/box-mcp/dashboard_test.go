package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// fixtureSnapshot is a synthetic obs-metrics content blob matching spec a5,
// used to drive renderDashboardHTML without touching the live observation
// plane.
const fixtureSnapshot = `{
  "instance": "100.83.33.126:7777",
  "host": "100.83.33.126",
  "taken_at": "20260529T083205Z",
  "capacity": {"disk_used": 213608263680, "disk_total": 494384795648,
    "box_home_bytes": 19034112, "blob_count": 5, "blob_bytes": 476,
    "proc_rss_mb": 222.3, "proc_cpu": 0.2, "proc_uptime_s": 276090},
  "tasks": {"total": 8, "by_status": {"✓": 6, "→": 2}, "completion_rate": 0.75, "stuck": 1},
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

func decodeFixture(t *testing.T) snapshot {
	t.Helper()
	var s snapshot
	if err := json.Unmarshal([]byte(fixtureSnapshot), &s); err != nil {
		t.Fatalf("fixture unmarshal: %v", err)
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
		"完整度",                // task completeness label
		"75%",                // completion_rate 0.75 -> 75%
		"卡住",                 // stuck label
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
