package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/windborneos/box-model/box"
)

// dialServer wires a freshly-built MCP server (over a brand-new tmp store) to
// an in-memory client and returns the live client session. The tmp dir is
// cleaned up on test end. Tests should defer cs.Close().
func dialServer(t *testing.T, owner string) (*mcp.ClientSession, context.Context) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("BOX_HOME", root)
	cfg := config{boxHome: root, owner: owner, disableObs: true}
	ctx := context.Background()
	svc, _, err := buildService(ctx, cfg, discard{})
	if err != nil {
		t.Fatalf("buildService: %v", err)
	}
	srv := buildServer(svc, cfg)

	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, ctx
}

// discard is an io.Writer that drops everything (used so test runs don't spam
// stderr with the MemObserver's logs). Tests pass disableObs=true so it would
// only be written to during service construction.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// callTool is a thin wrapper that fails the test if the tool errors at the
// transport layer (not the IsError=true case — those are returned as a
// success result and asserted elsewhere).
func callTool(t *testing.T, cs *mcp.ClientSession, ctx context.Context, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %q: %v", name, err)
	}
	return res
}

// unmarshalStructured pulls StructuredContent off a CallToolResult into v.
// Falls back to parsing the first TextContent block when StructuredContent is
// empty (the SDK only auto-fills it when the handler returns a typed Out).
func unmarshalStructured(t *testing.T, res *mcp.CallToolResult, v any) {
	t.Helper()
	if res.StructuredContent != nil {
		data, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatalf("marshal structured: %v", err)
		}
		if err := json.Unmarshal(data, v); err != nil {
			t.Fatalf("unmarshal into %T: %v\nraw: %s", v, err, data)
		}
		return
	}
	if len(res.Content) == 0 {
		t.Fatalf("no content in result")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	if err := json.Unmarshal([]byte(tc.Text), v); err != nil {
		t.Fatalf("unmarshal text: %v\nraw: %s", err, tc.Text)
	}
}

// 1) ListTools returns the 26 expected box_* tools and nothing else.
// R0.13.2: dropped box_create_task / box_get_task / box_set_task_status;
// added box_set_item_symbols; renamed box_append_task_trace → box_append_event
// and box_list_task_trace → box_list_events. Total: 28 → 26.
func TestMCPListTools(t *testing.T) {
	cs, ctx := dialServer(t, "trine")
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := map[string]bool{
		"box_create_box":        true,
		"box_get_box_by_key":    true,
		"box_seal_box":          true,
		"box_summary":           true,
		"box_store":             true,
		"box_replace_item":      true,
		"box_update_labels":     true,
		"box_merge_labels":      true,
		"box_remove_labels":     true,
		"box_delete_item":       true,
		"box_consume":           true,
		"box_show":              true,
		"box_browse":            true,
		"box_list_consumes":     true,
		"box_trace":             true,
		"box_legend":            true,
		"box_neighbors":         true,
		"box_set_item_symbols":  true,
		"box_append_event":      true,
		"box_list_events":       true,
		"box_task_start":        true,
		"box_task_finish":       true,
		"box_task_abort":        true,
		"box_task_token_status": true,
		"box_manual":            true,
		"box_legend_all":        true,
		"box_overview":          true,
		"box_gc_blobs":          true,
		"box_observability":     true,
		"box_set_box_labels":    true,
		"box_globes":            true,
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	if len(got) != len(want) {
		t.Errorf("expected %d tools, got %d: %v", len(want), len(got), got)
	}
	for name := range want {
		if !got[name] {
			t.Errorf("missing expected tool %q", name)
		}
	}
	for name := range got {
		if !want[name] {
			t.Errorf("unexpected tool %q exposed", name)
		}
	}
}

// 2) box_create_box round-trips a Box JSON struct.
func TestMCPCallCreateBox(t *testing.T) {
	cs, ctx := dialServer(t, "trine")
	res := callTool(t, cs, ctx, "box_create_box", map[string]any{
		"key":      "t",
		"owner_id": "x",
	})
	if res.IsError {
		t.Fatalf("unexpected IsError: %+v", res.Content)
	}
	var got box.Box
	unmarshalStructured(t, res, &got)
	if got.Key != "t" || got.OwnerID != "x" {
		t.Errorf("got %+v", got)
	}
	if got.ID == "" {
		t.Errorf("expected box id, got empty")
	}
}

// 3) Create → store → browse round-trip exposes the inserted item.
func TestMCPCallStoreAndBrowse(t *testing.T) {
	cs, ctx := dialServer(t, "alice")
	res := callTool(t, cs, ctx, "box_create_box", map[string]any{"key": "lab", "owner_id": "alice"})
	if res.IsError {
		t.Fatalf("create_box error: %+v", res.Content)
	}
	var b box.Box
	unmarshalStructured(t, res, &b)

	res = callTool(t, cs, ctx, "box_store", map[string]any{
		"box_id":      b.ID,
		"kind":        "memo",
		"source_type": "user",
		"storage_uri": "row://lab/1",
		"symbols":     []map[string]any{{"kind": "kind", "value": "M"}},
	})
	if res.IsError {
		t.Fatalf("store error: %+v", res.Content)
	}
	var item box.Item
	unmarshalStructured(t, res, &item)
	if item.ID == "" {
		t.Fatalf("expected item id, got empty")
	}

	res = callTool(t, cs, ctx, "box_browse", map[string]any{"box_id": b.ID})
	if res.IsError {
		t.Fatalf("browse error: %+v", res.Content)
	}
	var out browseOutput
	unmarshalStructured(t, res, &out)
	found := false
	for _, it := range out.Items {
		if it.ID == item.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("stored item %s not in browse result %+v", item.ID, out.Items)
	}
}

// 4) Trace across boxes returns items matching the kind=R filter from every
// box (boxKey == "").
func TestMCPTraceCrossBox(t *testing.T) {
	cs, ctx := dialServer(t, "alice")
	mkBox := func(key string) box.Box {
		res := callTool(t, cs, ctx, "box_create_box", map[string]any{"key": key, "owner_id": "alice"})
		if res.IsError {
			t.Fatalf("create_box %s: %+v", key, res.Content)
		}
		var b box.Box
		unmarshalStructured(t, res, &b)
		return b
	}
	a := mkBox("ba")
	bbb := mkBox("bb")
	storeR := func(bx box.Box, uri string) {
		res := callTool(t, cs, ctx, "box_store", map[string]any{
			"box_id":      bx.ID,
			"kind":        "req",
			"source_type": "user",
			"storage_uri": uri,
			"symbols":     []map[string]any{{"kind": "kind", "value": "R"}},
		})
		if res.IsError {
			t.Fatalf("store: %+v", res.Content)
		}
	}
	storeR(a, "row://ba/1")
	storeR(bbb, "row://bb/1")

	res := callTool(t, cs, ctx, "box_trace", map[string]any{
		"kind":  []string{"kind"},
		"value": []string{"R"},
	})
	if res.IsError {
		t.Fatalf("trace error: %+v", res.Content)
	}
	var out traceOutput
	unmarshalStructured(t, res, &out)
	if len(out.Items) != 2 {
		t.Errorf("expected 2 items across both boxes, got %d: %+v", len(out.Items), out.Items)
	}
}

// 5) box_legend on a built-in kind symbol returns the Decision documentation.
func TestMCPLegendBuiltin(t *testing.T) {
	cs, ctx := dialServer(t, "alice")
	res := callTool(t, cs, ctx, "box_legend", map[string]any{
		"kind":  "kind",
		"value": "D",
	})
	if res.IsError {
		t.Fatalf("legend error: %+v", res.Content)
	}
	var item box.Item
	unmarshalStructured(t, res, &item)
	// Content carries the symbolDef JSON; meaning="Decision" must appear.
	var payload struct {
		Meaning string `json:"meaning"`
		Value   string `json:"value"`
		Kind    string `json:"kind"`
	}
	if err := json.Unmarshal(item.Content, &payload); err != nil {
		t.Fatalf("unmarshal legend content: %v (raw=%s)", err, item.Content)
	}
	if payload.Meaning != "Decision" {
		t.Errorf("expected meaning=Decision, got %+v", payload)
	}
}

// 6) Neighbors walks one relation edge from a depends-on b.
func TestMCPNeighbors(t *testing.T) {
	cs, ctx := dialServer(t, "alice")
	res := callTool(t, cs, ctx, "box_create_box", map[string]any{"key": "g", "owner_id": "alice"})
	if res.IsError {
		t.Fatalf("create: %+v", res.Content)
	}
	var b box.Box
	unmarshalStructured(t, res, &b)
	store := func(uri string, syms []map[string]any) box.Item {
		res := callTool(t, cs, ctx, "box_store", map[string]any{
			"box_id":      b.ID,
			"kind":        "task",
			"source_type": "user",
			"storage_uri": uri,
			"symbols":     syms,
		})
		if res.IsError {
			t.Fatalf("store: %+v", res.Content)
		}
		var item box.Item
		unmarshalStructured(t, res, &item)
		return item
	}
	bItem := store("row://g/b", []map[string]any{{"kind": "kind", "value": "T"}})
	aItem := store("row://g/a", []map[string]any{
		{"kind": "kind", "value": "T"},
		{"kind": "relation", "value": "&", "ref": bItem.ID},
	})

	res = callTool(t, cs, ctx, "box_neighbors", map[string]any{
		"item_id": aItem.ID,
		"hops":    1,
	})
	if res.IsError {
		t.Fatalf("neighbors error: %+v", res.Content)
	}
	var sub box.Subgraph
	unmarshalStructured(t, res, &sub)
	if sub.Center != aItem.ID {
		t.Errorf("expected center=%s, got %s", aItem.ID, sub.Center)
	}
	foundB := false
	for _, n := range sub.Nodes {
		if n.ItemID == bItem.ID {
			foundB = true
			break
		}
	}
	if !foundB {
		t.Errorf("expected neighbor %s in subgraph, got %+v", bItem.ID, sub.Nodes)
	}
}

// 7) box.Service validation errors surface as IsError=true with the
// "validation" sentinel preserved in the message. We send key="" so the
// SDK's own JSON-schema check (which only enforces presence of "key") passes
// and the box.Service.CreateBox path runs, returning ErrValidation.
func TestMCPErrorPropagation(t *testing.T) {
	cs, ctx := dialServer(t, "alice")
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "box_create_box",
		Arguments: map[string]any{"key": ""},
	})
	if err != nil {
		t.Fatalf("CallTool transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true on empty key, got %+v", res)
	}
	if len(res.Content) == 0 {
		t.Fatalf("expected error content")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	if !strings.Contains(tc.Text, "validation") {
		t.Errorf("expected text to contain %q, got %q", "validation", tc.Text)
	}
}

// 9) R0.13.2 task-create-via-task_start + event append/list + symbol flip.
// Replaces TestMCPCreateTaskAndListTrace: box_create_task / box_append_task_
// trace / box_list_task_trace / box_set_task_status were all removed; the
// flow today is box_task_start (creates task + starts session) → box_append_
// event → box_list_events → box_set_item_symbols.
func TestMCPTaskStartAndEventFlow(t *testing.T) {
	cs, ctx := dialServer(t, "alice")

	res := callTool(t, cs, ctx, "box_create_box", map[string]any{"key": "tk", "owner_id": "alice"})
	if res.IsError {
		t.Fatalf("create_box: %+v", res.Content)
	}
	var b box.Box
	unmarshalStructured(t, res, &b)

	// box_task_start is the canonical task-creator. pass_criteria flows
	// through as opaque JSON — no Kind whitelist (R0.13.2 cleanup of #10).
	res = callTool(t, cs, ctx, "box_task_start", map[string]any{
		"box_id": b.ID,
		"intent": "ship feature",
		"goal":   []map[string]any{{"kind": "status", "value": "✓"}},
		"pass_criteria": map[string]any{
			"agent_invented_kind": "anything",
			"note":                "Box no longer policies this",
		},
		"nail_chain": []string{"database_engine_forge/a1"},
	})
	if res.IsError {
		if len(res.Content) > 0 {
			if tc, ok := res.Content[0].(*mcp.TextContent); ok {
				t.Fatalf("task_start error: %s", tc.Text)
			}
		}
		t.Fatalf("task_start error: %+v", res.Content)
	}
	var started struct {
		Task  box.Item `json:"task"`
		Token string   `json:"token"`
	}
	unmarshalStructured(t, res, &started)
	if started.Task.Kind != "task" {
		t.Errorf("expected kind=task, got %q", started.Task.Kind)
	}
	taskID := started.Task.ID

	// Two box_append_event calls (was box_append_task_trace).
	res = callTool(t, cs, ctx, "box_append_event", map[string]any{
		"item_id": taskID,
		"step":    map[string]any{"op": "store", "nail_ref": "database_engine_forge/a1"},
	})
	if res.IsError {
		t.Fatalf("append_event #1: %+v", res.Content)
	}
	res = callTool(t, cs, ctx, "box_append_event", map[string]any{
		"item_id": taskID,
		"step":    map[string]any{"op": "browse"},
	})
	if res.IsError {
		t.Fatalf("append_event #2: %+v", res.Content)
	}

	// box_list_events should return start + 2 events in order (steps 0,1,2).
	res = callTool(t, cs, ctx, "box_list_events", map[string]any{"item_id": taskID})
	if res.IsError {
		t.Fatalf("list_events: %+v", res.Content)
	}
	var out listEventsOutput
	unmarshalStructured(t, res, &out)
	if len(out.Events) != 3 {
		t.Fatalf("expected 3 events (task_start + 2 appended), got %d: %+v", len(out.Events), out.Events)
	}
	if out.Events[1].Op != "store" || out.Events[2].Op != "browse" {
		t.Errorf("ops out of order: %+v", out.Events)
	}

	// box_set_item_symbols (was box_set_task_status) flips status → ✓.
	res = callTool(t, cs, ctx, "box_set_item_symbols", map[string]any{
		"item_id": taskID,
		"symbols": []map[string]any{
			{"kind": "kind", "value": "T"},
			{"kind": "status", "value": "✓"},
		},
	})
	if res.IsError {
		t.Fatalf("set_item_symbols: %+v", res.Content)
	}
	var updated box.Item
	unmarshalStructured(t, res, &updated)
	foundDone := false
	for _, s := range updated.Symbols {
		if s.Kind == "status" && s.Value == "✓" {
			foundDone = true
		}
	}
	if !foundDone {
		t.Errorf("expected status=✓ in symbols, got %+v", updated.Symbols)
	}
}

// TestE2EBoxOverview — R5.1 box_overview wire-level smoke. Stays RED until
// task3 implements Service.Overview (today it returns ErrNotImplemented).
//
// Creates 2 boxes owned by alice, calls box_overview with axis=owner zoom=0,
// and asserts the JSON shape: axis="owner", total=2, histogram.alice=2.
func TestE2EBoxOverview(t *testing.T) {
	cs, ctx := dialServer(t, "alice")
	for _, k := range []string{"ov_a", "ov_b"} {
		res := callTool(t, cs, ctx, "box_create_box", map[string]any{"key": k, "owner_id": "alice"})
		if res.IsError {
			t.Fatalf("create_box %s: %+v", k, res.Content)
		}
	}
	res := callTool(t, cs, ctx, "box_overview", map[string]any{
		"axis": "owner",
		"zoom": 0,
	})
	if res.IsError {
		// Surface the actual error text so task3 sees what came back.
		if len(res.Content) > 0 {
			if tc, ok := res.Content[0].(*mcp.TextContent); ok {
				t.Fatalf("overview error: %s", tc.Text)
			}
		}
		t.Fatalf("overview error: %+v", res.Content)
	}
	var ov box.Overview
	unmarshalStructured(t, res, &ov)
	if ov.Axis != "owner" {
		t.Errorf("expected axis=owner, got %q", ov.Axis)
	}
	if ov.Total != 2 {
		t.Errorf("expected total=2, got %d", ov.Total)
	}
	if ov.Histogram == nil {
		t.Fatalf("expected histogram at zoom=0, got nil")
	}
	if ov.Histogram["alice"] != 2 {
		t.Errorf("expected histogram[alice]=2, got %d (full=%+v)", ov.Histogram["alice"], ov.Histogram)
	}
}

// 8) Confirm we do NOT expose the human-facing view/rotate surfaces (or any
// other tool outside the 22-tool whitelist).
func TestMCPViewNotExposed(t *testing.T) {
	cs, ctx := dialServer(t, "alice")
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	forbidden := map[string]bool{
		"box_view":       true,
		"box_rotate":     true,
		"box_list_boxes": true,
	}
	for _, tool := range res.Tools {
		if forbidden[tool.Name] {
			t.Errorf("tool %q is human-facing/deferred and must not be exposed", tool.Name)
		}
	}
}
