package main

import (
	"strings"
	"testing"
)

func TestElementOf(t *testing.T) {
	cases := map[string]string{
		"box.get_by_key": "风", "box.browse": "风", "box.summary": "风",
		"item.set_symbols": "火", "task.finish": "火",
		"item.store": "土", "box.create": "土", "event.append": "土",
		"http.blob": "水", "item.consume": "水",
		"box.gc_blobs": "空",
	}
	for op, want := range cases {
		if got := elementOf(op); got != want {
			t.Errorf("elementOf(%q)=%q want %q", op, got, want)
		}
	}
}

func TestFuzzyBand(t *testing.T) {
	for _, c := range []struct {
		f          float64
		word       string
	}{{0.6, "盛"}, {0.2, "温"}, {0.05, "微"}, {0, "静"}} {
		if _, w := fuzzyBand(c.f); w != c.word {
			t.Errorf("fuzzyBand(%v) word=%q want %q", c.f, w, c.word)
		}
	}
}

func TestDistillAndRender(t *testing.T) {
	snap := map[string]any{"counters": map[string]any{
		"box.get_by_key.attempt": 10.0, "box.get_by_key.success": 10.0,
		"item.store.attempt": 2.0, "item.store.success": 2.0,
		"item.get.attempt": 3.0, "item.get.error|err_type=notfound": 3.0,
	}}
	st := distill("mac-7777", "20260529T00Z", snap)
	if st.Pulse["风"] <= st.Pulse["土"] {
		t.Errorf("风 should dominate (10 vs 2): %v", st.Pulse)
	}
	if st.Ailing != 1 || len(st.AilOps) != 1 || st.AilOps[0] != "item.get" {
		t.Errorf("item.get should be ailing(100%% err): %+v", st)
	}
	if st.Healthy != 2 {
		t.Errorf("healthy=%d want 2", st.Healthy)
	}
	html := renderDashboardHTML([]instanceState{st})
	for _, want := range []string{"觉痕仪表盘", "风", "感知", "✗ 1 病", "item.get", "mac-7777", "text/html\"" /*not*/ [:0] + "活化态"} {
		if want != "" && !strings.Contains(html, want) {
			t.Errorf("dashboard HTML missing %q", want)
		}
	}
	// empty state
	if !strings.Contains(renderDashboardHTML(nil), "暂无快照") {
		t.Errorf("empty state missing placeholder")
	}
}
