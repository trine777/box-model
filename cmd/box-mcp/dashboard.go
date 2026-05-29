package main

// R14: human-observable dashboard. The observation plane is a human↔machine
// collaboration surface — agents query symbols over MCP; humans open a
// browser. Both read the SAME symbolic truth (觉痕 / 五元素 / priority),
// just rendered differently. This is the payoff of symbol-homology: 觉痕
// ✓~✗◯ and 风火土水空 are a shared human-machine language — a human needs
// no counter literacy to read "风 ●●● 盛".
//
// GET /dashboard renders an HTML 觉痕 portrait from the obs-fleet box's
// latest snapshot per instance. Mounted behind the same trust-tailnet
// Bearer middleware, so any tailnet device's browser can open it
// token-free.

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

// fiveElements maps an operation name to a fengyin mind element (SoR:
// fengyin/010-mind-five-elements). Keep in sync with scripts/boxlife.
var fiveElements = []struct {
	Sym  string
	Name string
	Kws  []string
}{
	{"风", "感知", []string{"get", "show", "browse", "trace", "globes", "summary", "list", "legend", "neighbor", "overview", "observ", "token_status"}},
	{"火", "判断", []string{"set_symbols", "set_box_symbols", "finish", "abort", "set_task", "seal"}},
	{"土", "成形", []string{"store", "create", "replace", "append"}},
	{"水", "连接", []string{"blob", "upload", "download", "consume", "sync", "export", "import", "items_blob"}},
	{"空", "治理", []string{"gc", "manual", "lint"}},
}

func elementOf(op string) string {
	f := strings.ToLower(op)
	// 空(治理)优先:gc/manual/lint 的主语义是治理,即便字面含 "blob"
	// (gc_blobs) 也不归水。Specific-intent wins over substring collision.
	for _, kw := range []string{"gc", "manual", "lint"} {
		if strings.Contains(f, kw) {
			return "空"
		}
	}
	for _, el := range fiveElements {
		for _, kw := range el.Kws {
			if strings.Contains(f, kw) {
				return el.Sym
			}
		}
	}
	return "风"
}

// fuzzyBand turns a fraction into the (glyph, word) fuzzy activity band —
// same 模糊数学 banding as boxlife.
func fuzzyBand(frac float64) (string, string) {
	switch {
	case frac >= 0.40:
		return "●●●", "盛"
	case frac >= 0.15:
		return "●●○", "温"
	case frac > 0:
		return "●○○", "微"
	default:
		return "○○○", "静"
	}
}

// instanceState is one business instance's distilled portrait.
type instanceState struct {
	Instance string
	TakenAt  string
	Pulse    map[string]float64 // element → fraction
	Healthy  int
	Partial  int
	Ailing   int
	AilOps   []string
}

// distill reads a fleet snapshot's counters into a portrait (five-element
// pulse + 觉痕 health), reusing the same logic as boxlife/boxstate.
func distill(instance, takenAt string, snapshot map[string]any) instanceState {
	st := instanceState{Instance: instance, TakenAt: takenAt, Pulse: map[string]float64{}}
	counters, _ := snapshot["counters"].(map[string]any)
	byEl := map[string]float64{}
	total := 0.0
	// per-op attempt/error to compute health
	type ae struct{ attempt, error float64 }
	ops := map[string]*ae{}
	for k, v := range counters {
		fv, _ := v.(float64)
		base := k
		if i := strings.Index(k, "|"); i >= 0 {
			base = k[:i]
		}
		dot := strings.LastIndex(base, ".")
		if dot < 0 {
			continue
		}
		op, kind := base[:dot], base[dot+1:]
		if ops[op] == nil {
			ops[op] = &ae{}
		}
		switch kind {
		case "attempt":
			ops[op].attempt += fv
			byEl[elementOf(op)] += fv
			total += fv
		case "error":
			ops[op].error += fv
		}
	}
	for _, el := range fiveElements {
		if total > 0 {
			st.Pulse[el.Sym] = byEl[el.Sym] / total
		}
	}
	for op, a := range ops {
		if a.attempt == 0 {
			continue
		}
		r := a.error / a.attempt
		switch {
		case r == 0:
			st.Healthy++
		case r < 0.30:
			st.Partial++
		default:
			st.Ailing++
			st.AilOps = append(st.AilOps, op)
		}
	}
	sort.Strings(st.AilOps)
	return st
}

// registerDashboard mounts GET /dashboard on mux. svc reads the obs-fleet
// box of THIS instance (the observation plane). On a business instance with
// no obs-fleet box it renders an empty-state page.
func registerDashboard(mux *http.ServeMux, svc *box.Service, caller string) {
	mux.HandleFunc("/dashboard", func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		states := collectFleetStates(ctx, svc, caller)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(renderDashboardHTML(states)))
	})
}

func collectFleetStates(ctx context.Context, svc *box.Service, caller string) []instanceState {
	b, err := svc.GetBoxByKey(ctx, caller, "obs-fleet")
	if err != nil {
		return nil
	}
	items, err := svc.Browse(ctx, b.ID, box.BrowseFilter{Limit: 5000})
	if err != nil {
		return nil
	}
	// latest snapshot per instance
	latest := map[string]box.Item{}
	for _, it := range items {
		var c map[string]any
		if json.Unmarshal(it.Content, &c) != nil {
			continue
		}
		inst, _ := c["instance"].(string)
		ta, _ := c["taken_at"].(string)
		if cur, ok := latest[inst]; !ok || ta > snapTakenAt(cur) {
			latest[inst] = it
		}
	}
	out := []instanceState{}
	for inst, it := range latest {
		var c map[string]any
		_ = json.Unmarshal(it.Content, &c)
		ta, _ := c["taken_at"].(string)
		snap, _ := c["snapshot"].(map[string]any)
		out = append(out, distill(inst, ta, snap))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Instance < out[j].Instance })
	return out
}

func snapTakenAt(it box.Item) string {
	var c map[string]any
	if json.Unmarshal(it.Content, &c) == nil {
		if ta, ok := c["taken_at"].(string); ok {
			return ta
		}
	}
	return ""
}

func renderDashboardHTML(states []instanceState) string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html lang="zh"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta http-equiv="refresh" content="30">
<title>box 系统现状 · 觉痕仪表盘</title><style>
body{background:#0f1115;color:#e6e6e6;font:15px/1.6 -apple-system,system-ui,monospace;margin:0;padding:24px}
h1{font-size:18px;font-weight:600;color:#9ef;margin:0 0 4px}
.sub{color:#778;font-size:12px;margin-bottom:20px}
.card{background:#171a21;border:1px solid #232733;border-radius:10px;padding:18px 22px;margin:0 0 16px;max-width:680px}
.inst{color:#9ef;font-weight:600;margin-bottom:10px}
.el{display:inline-block;width:128px;margin:2px 0}
.glyph{color:#5cf;letter-spacing:2px}
.dim{color:#667}.ok{color:#6e6}.warn{color:#fc6}.bad{color:#f66}
.big{font-size:15px}
table{border-collapse:collapse}td{padding:2px 10px 2px 0}
.legend{color:#667;font-size:11px;margin-top:18px;max-width:680px}
</style></head><body>`)
	b.WriteString(`<h1>box 系统现状 · 觉痕仪表盘</h1>`)
	b.WriteString(`<div class="sub">人机协同观察面 · 符号同源(觉痕/五元素)· 模糊非精确 · 每 30s 自刷新</div>`)
	if len(states) == 0 {
		b.WriteString(`<div class="card">◯ 观察平面暂无快照。运行 <code>boxsnap</code>(或等 com.box-metrics timer)把业务实例 obs 拉进 obs-fleet box。</div>`)
		b.WriteString(`</body></html>`)
		return b.String()
	}
	for _, st := range states {
		b.WriteString(`<div class="card">`)
		fmt.Fprintf(&b, `<div class="inst">◐ %s</div>`, html.EscapeString(st.Instance))
		// five-element pulse
		b.WriteString(`<div class="big">活法 · 五元素脉搏</div><div style="margin:6px 0">`)
		hot := []string{}
		for _, el := range fiveElements {
			g, word := fuzzyBand(st.Pulse[el.Sym])
			cls := "dim"
			if word == "盛" || word == "温" {
				cls = "glyph"
				hot = append(hot, el.Sym)
			}
			fmt.Fprintf(&b, `<span class="el"><b>%s</b> %s <span class="%s">%s</span> %s</span>`,
				el.Sym, el.Name, cls, g, word)
		}
		b.WriteString(`</div>`)
		phase := strings.Join(hot, "")
		if phase == "" {
			phase = "—"
		}
		fmt.Fprintf(&b, `<div class="dim" style="margin-bottom:12px">活化态: %s 主导</div>`, phase)
		// health 觉痕
		b.WriteString(`<div class="big">健康 · 觉痕</div><div style="margin:6px 0">`)
		fmt.Fprintf(&b, `<span class="ok">✓ %d 健康</span>&nbsp;&nbsp;`, st.Healthy)
		fmt.Fprintf(&b, `<span class="warn">~ %d 亚</span>&nbsp;&nbsp;`, st.Partial)
		ailcls := "ok"
		if st.Ailing > 0 {
			ailcls = "bad"
		}
		fmt.Fprintf(&b, `<span class="%s">✗ %d 病</span>`, ailcls, st.Ailing)
		if len(st.AilOps) > 0 {
			fmt.Fprintf(&b, ` <span class="dim">(%s)</span>`, html.EscapeString(strings.Join(st.AilOps, ", ")))
		}
		b.WriteString(`</div>`)
		fmt.Fprintf(&b, `<div class="dim" style="margin-top:10px">快照 %s</div>`, html.EscapeString(st.TakenAt))
		b.WriteString(`</div>`)
	}
	b.WriteString(`<div class="legend">活力 ●●●盛 ●●○温 ●○○微 ○○○静 · 健康 ✓好 ~亚 ✗病 ◯未用 · 五元素=fengyin 心智(风感知/火判断/土成形/水连接/空治理)· agent 用 boxops/MCP 查同源符号</div>`)
	b.WriteString(`</body></html>`)
	return b.String()
}
