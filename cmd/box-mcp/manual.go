package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/windborneos/box-model/box"
)

// manualMarkdown is the self-describing traffic manual returned by the
// box_manual MCP tool. Fresh agents that connect to box-mcp can call this
// once to learn the symbol system, the 程辙 (program-track) flow, and the
// 28-tool surface. Keep this file the single source of truth — README and
// docs/mcp.md cross-reference it but don't duplicate the prose.
const manualMarkdown = `# box-mcp 交通手册 (Traffic Manual)

box-model is a single-tenant KM (knowledge management) store. Items live in
boxes; each item carries free-form ` + "`labels`" + ` (string→string) plus a
structured ` + "`symbols`" + ` array (the "路标" / AI-facing routing layer).

## Five concepts to know

1. **Box** = a named container. Created with ` + "`box_create_box`" + `.
2. **Item** = a typed row inside a box. Stored with ` + "`box_store`" + `.
3. **Symbol** = ` + "`{kind, value, ref?}`" + `. Drives ` + "`box_trace`" + `,
   ` + "`box_neighbors`" + `, and status flips.
4. **Task** = an item with kind=task carrying intent/goal/pass_criteria/
   nail_chain. Created with ` + "`box_create_task`" + `.
5. **程辙 / YiCheng** = a session-scoped task lifecycle. Open with
   ` + "`box_task_start`" + ` (returns a token), append events with
   ` + "`box_append_task_trace`" + `, close with ` + "`box_task_finish`" + `
   or ` + "`box_task_abort`" + `. The model is a **path ledger**, not a
   transaction — 合程 (finish) is just one more append; it does NOT freeze
   the task.

## Three invariants (read these once, internalize forever)

- **#10 Box is dumb storage.** Schema validation only; never interprets
  ` + "`pass_criteria.query`" + `. That's the agent's job.
- **#11 Token = session, not authorization.** ` + "`tsk_…`" + ` tokens
  identify a live 程辙 session in memory. They are NOT ACLs.
- **#12 Box is 符径 (Symbol Path), not a database.** Append-only path
  ledger. 觉痕 (awareness markers: ✓ / ✗ / ? / → / ~ / ◯) can be overwritten
  by subsequent events.

## Native symbols (25)

Call ` + "`box_legend_all`" + ` for the full machine-readable list. Cheat
sheet:

- **kind**: D=Decision · R=Requirement · Q=Question · H=Hypothesis ·
  T=Task · M=Memo · F=Fact · O=Observation · A=Action · X=External
- **status (觉痕)**: ? unknown · → in-flight · ✓ done · ✗ failed ·
  ~ partial · ◯ canceled
- **relation**: ` + "`>`" + ` blocks · ` + "`<`" + ` blocked-by · & with ·
  | xor · ≈ similar-to · ⊃ has-part
- **priority**: ` + "`*`" + ` low · ` + "`**`" + ` medium · ` + "`***`" + ` high
- **scope / topic / domain**: free-form (` + "`[A-Za-z0-9_-]+`" + `,
  ` + "`nf:<ns>`" + ` for domain)

## The 28 tools

### Box / Item CRUD (17)
- ` + "`box_create_box`" + ` — create a box (key, owner_type, owner_id)
- ` + "`box_get_box_by_key`" + ` — resolve box id by key
- ` + "`box_seal_box`" + ` — make read-only
- ` + "`box_summary`" + ` — counts by kind & label
- ` + "`box_store`" + ` — store an item (kind, source_type, storage_uri,
  content, symbols)
- ` + "`box_replace_item`" + ` — open a new revision
- ` + "`box_update_labels`" + ` / ` + "`box_merge_labels`" + ` /
  ` + "`box_remove_labels`" + ` — label edits
- ` + "`box_delete_item`" + ` — soft delete
- ` + "`box_consume`" + ` — audit a read (optionally mark consumed)
- ` + "`box_show`" + ` — fetch by id
- ` + "`box_browse`" + ` — list with filter
- ` + "`box_list_consumes`" + ` — audit log
- ` + "`box_trace`" + ` — Symbol-dimension query (kind/value/ref)
- ` + "`box_legend`" + ` — doc for one symbol literal
- ` + "`box_neighbors`" + ` — hop-bounded relation subgraph

### Task surface (R0.10) (5)
- ` + "`box_create_task`" + ` — task item with pass_criteria
- ` + "`box_set_task_status`" + ` — flip 觉痕 in-place
- ` + "`box_append_task_trace`" + ` — append a TraceStep
- ` + "`box_list_task_trace`" + ` — full trace history
- ` + "`box_get_task`" + ` — fetch task item

### 程辙 layer (R0.13.1) (4)
- ` + "`box_task_start`" + ` — 启程: create task + open session, returns
  ` + "`{task, token}`" + `. Token auto-injected into trace meta.
- ` + "`box_task_finish`" + ` — 合程: append a ✓ task_finish event (path
  ledger; NOT a freeze)
- ` + "`box_task_abort`" + ` — append a ✗ task_abort event, no rollback
- ` + "`box_task_token_status`" + ` — is this token still live?

### Self-describing (2)
- ` + "`box_manual`" + ` — this document
- ` + "`box_legend_all`" + ` — all 25 native symbols at once

## Minimal happy path (copy/paste-able)

` + "```json" + `
// 1. create box
{"tool":"box_create_box","args":{"key":"my-km","owner_type":"user","owner_id":"alice"}}

// 2. 启程 a task
{"tool":"box_task_start","args":{
  "box_id":"box_…",
  "intent":"draft Q3 strategy doc",
  "goal":[{"kind":"topic","value":"q3-strategy"}],
  "pass_criteria":{
    "kind":"exists",
    "query":{"kind":["topic"],"value":["q3-strategy"]},
    "reason":"box must have a topic=q3-strategy item when done"
  }
}}
// → returns {task, token}. Keep the token.

// 3. work, append trace
{"tool":"box_append_task_trace","args":{
  "task_id":"item_…",
  "step":{"op":"outline","nail_ref":"strategy_sop:outline"}
}}

// 4. store the artifact (the topic=q3-strategy item that satisfies pass_criteria)
{"tool":"box_store","args":{
  "box_id":"box_…",
  "kind":"M",                    // memo (use box_legend_all for the list)
  "source_type":"generated",
  "storage_uri":"row://docs/q3",
  "format":"markdown",
  "content":"# Q3 …",
  "symbols":[
    {"kind":"kind","value":"M"},
    {"kind":"topic","value":"q3-strategy"}
  ]
}}

// 5. 合程
{"tool":"box_task_finish","args":{"token":"tsk_…","status":"✓","summary":"draft shipped"}}
` + "```" + `

## Pitfalls

- ` + "`storage_uri`" + ` schemes are whitelisted: ` + "`row://`" + `,
  ` + "`blob://`" + `, ` + "`folder://`" + `, ` + "`repo://`" + `,
  ` + "`s3://`" + `, ` + "`ipfs://`" + `, ` + "`collection://`" + `. No
  ` + "`inline://`" + `.
- Every item must carry at least one ` + "`kind`" + ` symbol from the
  whitelist (D/R/Q/H/T/M/F/O/A/X).
- ` + "`pass_criteria.kind`" + ` ∈ {exists, absent, all_match, count_eq,
  compound}. Box validates the schema; it does NOT execute the query — the
  agent does.
- 合程 (finish) is NOT a freeze. ` + "`SetItemSymbols`" + ` can still
  overwrite 觉痕 afterwards. This is invariant #12.
- For HTTP mode, every request needs ` + "`Authorization: Bearer $BOX_API_TOKEN`" + `.

⭐ Source: github.com/trine777/box-model
`

type manualOutput struct {
	Format  string `json:"format"`
	Content string `json:"content"`
	Version string `json:"version"`
}

func (h *handlers) handleManual(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	return nil, manualOutput{
		Format:  "markdown",
		Content: manualMarkdown,
		Version: "0.13.1",
	}, nil
}

// allNativeSymbols enumerates the 25 native (whitelist-driven) symbols. Free-
// form symbol kinds (scope/topic/domain) are excluded because their value
// space is open.
var allNativeSymbols = []box.Symbol{
	{Kind: box.SymKind, Value: "D"}, {Kind: box.SymKind, Value: "R"},
	{Kind: box.SymKind, Value: "Q"}, {Kind: box.SymKind, Value: "H"},
	{Kind: box.SymKind, Value: "T"}, {Kind: box.SymKind, Value: "M"},
	{Kind: box.SymKind, Value: "F"}, {Kind: box.SymKind, Value: "O"},
	{Kind: box.SymKind, Value: "A"}, {Kind: box.SymKind, Value: "X"},
	{Kind: box.SymStatus, Value: "?"}, {Kind: box.SymStatus, Value: "→"},
	{Kind: box.SymStatus, Value: "✓"}, {Kind: box.SymStatus, Value: "✗"},
	{Kind: box.SymStatus, Value: "~"}, {Kind: box.SymStatus, Value: "◯"},
	{Kind: box.SymRelation, Value: ">"}, {Kind: box.SymRelation, Value: "<"},
	{Kind: box.SymRelation, Value: "&"}, {Kind: box.SymRelation, Value: "|"},
	{Kind: box.SymRelation, Value: "≈"}, {Kind: box.SymRelation, Value: "⊃"},
	{Kind: box.SymPriority, Value: "*"}, {Kind: box.SymPriority, Value: "**"},
	{Kind: box.SymPriority, Value: "***"},
}

type legendAllOutput struct {
	Count   int        `json:"count"`
	Symbols []box.Item `json:"symbols"`
}

func (h *handlers) handleLegendAll(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	items := make([]box.Item, 0, len(allNativeSymbols))
	for _, sym := range allNativeSymbols {
		// SymRelation legend lookups require a Ref; the bootstrap stores them
		// with an empty Ref, so pass through verbatim. LegendOf normalises the
		// idem key by kind+value.
		s := sym
		s.Ref = ""
		item, err := h.svc.LegendOf(ctx, h.caller, s)
		if err != nil {
			continue
		}
		items = append(items, item)
	}
	return nil, legendAllOutput{Count: len(items), Symbols: items}, nil
}
