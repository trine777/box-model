package main

// R15: human-observable engineering dashboard (obs v2, docs/obs_v2_spec.md a6).
//
// v1 (R14) rendered a 五元素/觉痕 symbolic portrait that was judged unusable —
// philosophy, not engineering reality. v2 throws out the symbol abstraction and
// shows ONLY real physical + business numbers: machine capacity, task execution
// completeness, request performance, and per-box usage/freshness.
//
// GET /dashboard renders an HTML number-table from the obs-metrics box's latest
// snapshot per instance (the observation plane :7788, NOT the old obs-fleet
// symbolic box). Mounted behind the trust-tailnet Bearer middleware, so any
// tailnet device's browser can open it token-free. Fly is NOT in the fleet.
//
// Schema consumed (obs_v2_spec.md a5, obs-metrics item content):
//
//	{instance, host, taken_at,
//	 capacity:{disk_used,disk_total,box_home_bytes,blob_count,blob_bytes,
//	           proc_rss_mb,proc_cpu,proc_uptime_s},
//	 tasks:{total,by_status{},completion_rate,stuck},
//	 perf:{ops:[{op,calls,errors,err_pct,err_types{},avg_ms,p95_ms}]},
//	 business:{box_count,item_total,per_box:[{key,items,latest_stored_at,uses}]}}

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"sort"
	"strings"

	"github.com/windborneos/box-model/box"
)

// expectedFleet is the set of fleet members obs v2 watches (spec a1). Any
// expected member missing from the latest obs-metrics snapshots is rendered as
// "down". Fly is deliberately excluded.
var expectedFleet = []string{"100.83.33.126:7777", "100.83.33.126:7788"}

// snapshot is one instance's decoded obs-metrics content (spec a5).
type snapshot struct {
	Instance string `json:"instance"`
	Host     string `json:"host"`
	TakenAt  string `json:"taken_at"`
	Capacity struct {
		DiskUsed     float64 `json:"disk_used"`
		DiskTotal    float64 `json:"disk_total"`
		BoxHomeBytes float64 `json:"box_home_bytes"`
		BlobCount    float64 `json:"blob_count"`
		BlobBytes    float64 `json:"blob_bytes"`
		ProcRSSMb    float64 `json:"proc_rss_mb"`
		ProcCPU      float64 `json:"proc_cpu"`
		ProcUptimeS  float64 `json:"proc_uptime_s"`
	} `json:"capacity"`
	Tasks struct {
		Total          int            `json:"total"`
		ByStatus       map[string]int `json:"by_status"`
		CompletionRate float64        `json:"completion_rate"`
		Stuck          int            `json:"stuck"`
		DurationsMs    []float64      `json:"durations_ms"`
	} `json:"tasks"`
	Perf struct {
		Ops []struct {
			Op       string         `json:"op"`
			Calls    int            `json:"calls"`
			Errors   int            `json:"errors"`
			ErrPct   float64        `json:"err_pct"`
			ErrTypes map[string]int `json:"err_types"`
			AvgMs    float64        `json:"avg_ms"`
			P95Ms    float64        `json:"p95_ms"`
		} `json:"ops"`
	} `json:"perf"`
	Business struct {
		BoxCount  int `json:"box_count"`
		ItemTotal int `json:"item_total"`
		PerBox    []struct {
			Key            string `json:"key"`
			Items          int    `json:"items"`
			LatestStoredAt string `json:"latest_stored_at"`
			Uses           int    `json:"uses"`
		} `json:"per_box"`
	} `json:"business"`
}

// registerDashboard mounts GET /dashboard on mux. svc reads the obs-metrics box
// of THIS instance (the observation plane). With no obs-metrics box it renders
// an empty-state page.
func registerDashboard(mux *http.ServeMux, svc *box.Service, caller string) {
	mux.HandleFunc("/dashboard", func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		snaps := collectSnapshots(ctx, svc, caller)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(renderDashboardHTML(snaps)))
	})
}

// collectSnapshots reads the obs-metrics box and returns the latest snapshot
// per instance (by taken_at), sorted by instance.
func collectSnapshots(ctx context.Context, svc *box.Service, caller string) []snapshot {
	b, err := svc.GetBoxByKey(ctx, caller, "obs-metrics")
	if err != nil {
		return nil
	}
	items, err := svc.Browse(ctx, b.ID, box.BrowseFilter{Limit: 5000})
	if err != nil {
		return nil
	}
	latest := map[string]snapshot{}
	for _, it := range items {
		var s snapshot
		if json.Unmarshal(it.Content, &s) != nil || s.Instance == "" {
			continue
		}
		if cur, ok := latest[s.Instance]; !ok || s.TakenAt > cur.TakenAt {
			latest[s.Instance] = s
		}
	}
	out := make([]snapshot, 0, len(latest))
	for _, s := range latest {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Instance < out[j].Instance })
	return out
}

// taskAgg is the SYSTEM-LEVEL task rollup (R15.1). task is a development
// concept that lives entirely on the business plane (:7777); per-instance task
// cards on the observation plane (:7788, 0 tasks) were misleading noise, so the
// task metric is promoted to a single aggregate over ALL instances' latest
// snapshots. Aggregation rule must stay identical across dashboard.go / boxops
// / boxboard (obs_v2_spec R15.1):
//   total          = Σ instance tasks.total
//   byStatus       = Σ instance tasks.by_status, merged per glyph
//   completionRate = byStatus["✓"] / total   (0 when total==0)
//   stuck          = Σ instance tasks.stuck
//   avgDurationS   = mean of all instances' tasks.durations_ms, in seconds
type taskAgg struct {
	Total          int
	ByStatus       map[string]int
	CompletionRate float64
	Stuck          int
	AvgDurationS   float64
}

// aggregateTasks rolls every instance's latest task snapshot into one
// system-level summary.
func aggregateTasks(snaps []snapshot) taskAgg {
	agg := taskAgg{ByStatus: map[string]int{}}
	var durSum float64
	var durN int
	for _, s := range snaps {
		t := s.Tasks
		agg.Total += t.Total
		agg.Stuck += t.Stuck
		for k, v := range t.ByStatus {
			agg.ByStatus[k] += v
		}
		for _, d := range t.DurationsMs {
			durSum += d
			durN++
		}
	}
	if agg.Total > 0 {
		agg.CompletionRate = float64(agg.ByStatus["✓"]) / float64(agg.Total)
	}
	if durN > 0 {
		agg.AvgDurationS = durSum / float64(durN) / 1000.0
	}
	return agg
}

func pct(num, den float64) float64 {
	if den <= 0 {
		return 0
	}
	return 100 * num / den
}

func humanBytes(b float64) string {
	const u = 1024.0
	switch {
	case b >= u*u*u:
		return fmt.Sprintf("%.1fG", b/(u*u*u))
	case b >= u*u:
		return fmt.Sprintf("%.1fM", b/(u*u))
	case b >= u:
		return fmt.Sprintf("%.1fK", b/u)
	default:
		return fmt.Sprintf("%.0fB", b)
	}
}

func humanDur(s float64) string {
	switch {
	case s >= 86400:
		return fmt.Sprintf("%.1fd", s/86400)
	case s >= 3600:
		return fmt.Sprintf("%.1fh", s/3600)
	case s >= 60:
		return fmt.Sprintf("%.0fm", s/60)
	default:
		return fmt.Sprintf("%.0fs", s)
	}
}

func renderDashboardHTML(snaps []snapshot) string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html lang="zh"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta http-equiv="refresh" content="30">
<title>box 观察平面 · 工程指标</title><style>
body{background:#0f1115;color:#e6e6e6;font:14px/1.55 -apple-system,system-ui,monospace;margin:0;padding:24px}
h1{font-size:18px;font-weight:600;color:#9ef;margin:0 0 4px}
.sub{color:#778;font-size:12px;margin-bottom:20px}
.card{background:#171a21;border:1px solid #232733;border-radius:10px;padding:16px 20px;margin:0 0 16px;max-width:860px}
.inst{color:#9ef;font-weight:600;margin-bottom:10px;font-size:15px}
.down{border-color:#5a2730}.down .inst{color:#f88}
.sys{border-color:#2d3a4a;background:#141b24}.sys .inst{color:#bdf}
.sect{color:#9bd;font-size:12px;letter-spacing:.5px;margin:14px 0 6px;text-transform:uppercase}
.kv{display:inline-block;min-width:150px;margin:2px 14px 2px 0}
.kv b{color:#cde}.dim{color:#778}
table{border-collapse:collapse;margin-top:4px;font-size:13px}
th{text-align:right;color:#89a;font-weight:600;padding:2px 12px 4px 0;border-bottom:1px solid #2a2f3b}
th.l,td.l{text-align:left}td{text-align:right;padding:2px 12px 2px 0}
.bad{color:#f66;font-weight:600}.warn{color:#fc6}.ok{color:#6e6}
.taken{color:#667;font-size:11px;margin-top:12px}
</style></head><body>`)
	b.WriteString(`<h1>box 观察平面 · 工程指标</h1>`)
	b.WriteString(`<div class="sub">真实物理 + 业务数字(obs-metrics 快照)· 舰队 :7777 业务 / :7788 观察 · Fly 不纳入 · 每 30s 自刷新</div>`)

	if len(snaps) == 0 {
		b.WriteString(`<div class="card">观察平面暂无快照。运行 <code>boxsnap</code>(或等 com.box-metrics timer)采集四类指标落入 obs-metrics box。</div>`)
		// still flag expected members as down for visibility
		for _, m := range expectedFleet {
			fmt.Fprintf(&b, `<div class="card down"><div class="inst">%s — down(无快照)</div></div>`, html.EscapeString(m))
		}
		b.WriteString(`</body></html>`)
		return b.String()
	}

	renderSystemTasks(&b, aggregateTasks(snaps))

	seen := map[string]bool{}
	for _, s := range snaps {
		seen[s.Instance] = true
		renderInstance(&b, s)
	}
	// expected-but-absent members → down
	for _, m := range expectedFleet {
		if !seen[m] {
			fmt.Fprintf(&b, `<div class="card down"><div class="inst">%s — down</div><div class="dim">期望成员但最新快照中缺席(未采集到 / 实例下线)。</div></div>`, html.EscapeString(m))
		}
	}
	b.WriteString(`</body></html>`)
	return b.String()
}

// renderSystemTasks emits the single system-level task summary block at the top
// of the page (R15.1). task is a development concept aggregated across the whole
// fleet, not a per-instance metric.
func renderSystemTasks(b *strings.Builder, a taskAgg) {
	b.WriteString(`<div class="card sys"><div class="inst">系统 task(全舰队聚合)</div>`)
	fmt.Fprintf(b, `<span class="kv">total <b>%d</b></span>`, a.Total)
	fmt.Fprintf(b, `<span class="kv">完整度 <b>%.0f%%</b></span>`, a.CompletionRate*100)
	stkcls := "ok"
	if a.Stuck > 0 {
		stkcls = "bad"
	}
	fmt.Fprintf(b, `<span class="kv">卡住 <b class="%s">%d</b></span>`, stkcls, a.Stuck)
	fmt.Fprintf(b, `<span class="kv">avg duration <b>%.0fs</b></span>`, a.AvgDurationS)
	if len(a.ByStatus) > 0 {
		keys := make([]string, 0, len(a.ByStatus))
		for k := range a.ByStatus {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s:%d", html.EscapeString(k), a.ByStatus[k]))
		}
		fmt.Fprintf(b, `<span class="kv">by_status <b>%s</b></span>`, strings.Join(parts, " "))
	} else {
		b.WriteString(`<span class="kv dim">by_status —</span>`)
	}
	b.WriteString(`</div>`)
}

func renderInstance(b *strings.Builder, s snapshot) {
	b.WriteString(`<div class="card">`)
	fmt.Fprintf(b, `<div class="inst">%s <span class="dim">(host %s)</span></div>`,
		html.EscapeString(s.Instance), html.EscapeString(s.Host))

	// --- capacity 容量 ---
	c := s.Capacity
	b.WriteString(`<div class="sect">容量 capacity</div>`)
	diskPct := pct(c.DiskUsed, c.DiskTotal)
	dcls := "ok"
	if diskPct >= 90 {
		dcls = "bad"
	} else if diskPct >= 75 {
		dcls = "warn"
	}
	fmt.Fprintf(b, `<span class="kv">磁盘 <b class="%s">%.1f%%</b> <span class="dim">(%s/%s)</span></span>`,
		dcls, diskPct, humanBytes(c.DiskUsed), humanBytes(c.DiskTotal))
	fmt.Fprintf(b, `<span class="kv">box_home <b>%s</b></span>`, humanBytes(c.BoxHomeBytes))
	fmt.Fprintf(b, `<span class="kv">blob <b>%.0f</b> <span class="dim">(%s)</span></span>`, c.BlobCount, humanBytes(c.BlobBytes))
	fmt.Fprintf(b, `<span class="kv">RSS <b>%.1f MB</b></span>`, c.ProcRSSMb)
	fmt.Fprintf(b, `<span class="kv">CPU <b>%.1f%%</b></span>`, c.ProcCPU)
	fmt.Fprintf(b, `<span class="kv">uptime <b>%s</b></span>`, humanDur(c.ProcUptimeS))

	// task is NOT rendered per-instance anymore — it is a development concept
	// aggregated to a single system-level summary at the top of the page
	// (renderSystemTasks). The instance card keeps 容量 / 性能 / 业务 only.

	// --- perf 性能表 ---
	b.WriteString(`<div class="sect">性能 perf(请求)</div>`)
	if len(s.Perf.Ops) == 0 {
		b.WriteString(`<div class="dim">无 op 计数(进程重启后清零)。</div>`)
	} else {
		ops := s.Perf.Ops
		sort.Slice(ops, func(i, j int) bool { return ops[i].Calls > ops[j].Calls })
		b.WriteString(`<table><tr><th class="l">op</th><th>calls</th><th>err%</th><th>avg ms</th><th>p95 ms</th><th class="l">err_types</th></tr>`)
		for _, op := range ops {
			ecls := ""
			if op.ErrPct >= 20 {
				ecls = "bad"
			} else if op.ErrPct > 0 {
				ecls = "warn"
			}
			et := ""
			if len(op.ErrTypes) > 0 {
				ks := make([]string, 0, len(op.ErrTypes))
				for k := range op.ErrTypes {
					ks = append(ks, k)
				}
				sort.Strings(ks)
				ps := make([]string, 0, len(ks))
				for _, k := range ks {
					ps = append(ps, fmt.Sprintf("%s:%d", html.EscapeString(k), op.ErrTypes[k]))
				}
				et = strings.Join(ps, " ")
			}
			fmt.Fprintf(b, `<tr><td class="l">%s</td><td>%d</td><td class="%s">%.0f%%</td><td>%.1f</td><td>%.1f</td><td class="l dim">%s</td></tr>`,
				html.EscapeString(op.Op), op.Calls, ecls, op.ErrPct, op.AvgMs, op.P95Ms, et)
		}
		b.WriteString(`</table>`)
	}

	// --- business 业务表 (per-box) ---
	bus := s.Business
	fmt.Fprintf(b, `<div class="sect">业务 business · %d box · %d item</div>`, bus.BoxCount, bus.ItemTotal)
	if len(bus.PerBox) == 0 {
		b.WriteString(`<div class="dim">无 per-box 数据。</div>`)
	} else {
		pb := bus.PerBox
		sort.Slice(pb, func(i, j int) bool {
			if pb[i].Items != pb[j].Items {
				return pb[i].Items > pb[j].Items
			}
			return pb[i].Key < pb[j].Key
		})
		b.WriteString(`<table><tr><th class="l">box key</th><th>items</th><th>使用次数</th><th class="l">latest</th></tr>`)
		for _, p := range pb {
			fmt.Fprintf(b, `<tr><td class="l">%s</td><td>%d</td><td>%d</td><td class="l dim">%s</td></tr>`,
				html.EscapeString(p.Key), p.Items, p.Uses, html.EscapeString(p.LatestStoredAt))
		}
		b.WriteString(`</table>`)
	}

	fmt.Fprintf(b, `<div class="taken">快照 %s</div>`, html.EscapeString(s.TakenAt))
	b.WriteString(`</div>`)
}
