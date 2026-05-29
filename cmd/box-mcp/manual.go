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

> **Read this section before calling any tool.** Most agents on first
> contact assume box-mcp is a database with extra ceremony. It is not.
> Using it that way will produce confused queries and frustrated retries.

## Fastest path: the one-line wrappers (R11)

Before hand-rolling the SSE handshake below, know that three shell
wrappers ship in ` + "`scripts/`" + ` (install to your PATH) that collapse all
of it to one line each. On the tailnet they need **no token**:

` + "```bash" + `
boxcall box_globes                      # any MCP tool, one line
boxcall box_show '{"item_id":"item_…"}' # tool + JSON args
boxput ./report.pdf media-archive       # upload bytes + register item → prints item_id
boxget item_… ./out.pdf                 # download an item's file
` + "```" + `

Env: ` + "`BOX_ENDPOINT`" + ` (default ` + "`http://100.83.33.126:7777`" + ` — the tailnet
host), ` + "`BOX_TOKEN`" + ` (only needed for the public Fly endpoint). These
wrappers handle initialize / Mcp-Session-Id / notifications/initialized /
SSE parsing for you. Hand-roll the protocol below only if you can't use
the wrappers.

## Streamable-HTTP MCP transport quickstart (read first if you're not Claude)

If you reached this server over HTTP (` + "`/mcp`" + ` endpoint), the wire format is
**JSON-RPC 2.0 over SSE**, not plain HTTP/JSON. Three things every fresh
client needs to get right:

1. **Initialize handshake first.** Send POST ` + "`/mcp`" + ` with body
   ` + "`{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2024-11-05\",\"capabilities\":{},\"clientInfo\":{\"name\":\"X\",\"version\":\"0.1\"}}}`" + `.
   Headers MUST include ` + "`Content-Type: application/json`" + ` AND
   ` + "`Accept: application/json, text/event-stream`" + ` (server only speaks SSE).

2. **Capture ` + "`Mcp-Session-Id`" + ` response header.** Server returns it on the
   initialize response; send it back as a request header on every
   subsequent call. Without it the next call starts a fresh session.

3. **Send ` + "`notifications/initialized`" + ` exactly once.** A JSON-RPC
   notification (no ` + "`id`" + ` field), method=` + "`notifications/initialized`" + `,
   params=` + "`{}`" + `, with the captured ` + "`Mcp-Session-Id`" + ` header. Server treats
   subsequent ` + "`tools/call`" + ` invocations as out-of-protocol if you skip this.

Response framing: every response is one or more SSE event blocks. Each
block has lines like ` + "`event: message\\ndata: {…json…}\\n\\n`" + `. The JSON-RPC
payload is the ` + "`data:`" + ` line — grep it, parse the JSON. ` + "`/healthz`" + ` (no
auth) is the only route that returns plain text.

curl template (all in one):

` + "```bash" + `
TOKEN="<bearer>"
URL="https://box-mcp-trine.fly.dev"

# Step 1: initialize, capture Mcp-Session-Id
curl -sS -D /tmp/h "$URL/mcp" \\
  -H "Authorization: Bearer $TOKEN" \\
  -H "Content-Type: application/json" \\
  -H "Accept: application/json, text/event-stream" \\
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0.1"}}}' \\
  > /dev/null
SID=$(grep -i "^Mcp-Session-Id:" /tmp/h | tr -d '\\r' | awk '{print $2}')

# Step 2: notifications/initialized (no id)
curl -sS "$URL/mcp" \\
  -H "Authorization: Bearer $TOKEN" \\
  -H "Content-Type: application/json" \\
  -H "Accept: application/json, text/event-stream" \\
  -H "Mcp-Session-Id: $SID" \\
  -d '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}' \\
  > /dev/null

# Step 3: now you can call tools. Parse the data: line out of SSE response.
curl -sS "$URL/mcp" \\
  -H "Authorization: Bearer $TOKEN" \\
  -H "Content-Type: application/json" \\
  -H "Accept: application/json, text/event-stream" \\
  -H "Mcp-Session-Id: $SID" \\
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"box_manual","arguments":{}}}' \\
  | grep "^data:" | sed 's/^data: //' | jq -r '.result.content[0].text'
` + "```" + `

If you skip step 2 you'll see server errors like "session not initialized";
if you skip the SSE ` + "`Accept:`" + ` you'll see 406 Not Acceptable.


## Mental model: this is NOT a database

box-model is a **符径 (Symbol Path)** — an append-only path ledger with a
typed symbol system bolted on. The closest analogue is not Postgres or
Mongo; it is closer to a journal + a tag graph. There is **no SQL, no
joins, no transactions, no cursors, no roles**. There is:

- **Append.** Every write is one more event. Old revisions are kept
  (` + "`is_latest=false`" + `).
- **Symbols.** Typed routing tags (kind/status/relation/scope/topic/
  priority/domain). The primary lookup axis — not labels, not text.
- **觉痕 (awareness markers).** A status symbol on an item is a *cursor*
  on its history, NOT a terminal state. Any later event can overwrite it.

## If you came from SQL, here is the translation

| SQL mental model | box-model term | Why different |
| --- | --- | --- |
| ` + "`SELECT … WHERE col=…`" + ` | ` + "`box_trace`" + ` (symbol query) or ` + "`box_browse`" + ` (label exact) | No general predicate engine. Index by symbol kind first. |
| ` + "`JOIN`" + ` | ` + "`box_neighbors`" + ` (hop-bounded relation BFS) | Relations are explicit ` + "`SymRelation`" + ` symbols, not foreign keys. |
| ` + "`BEGIN … COMMIT`" + ` | ` + "`box_task_start`" + ` → ` + "`box_task_finish`" + ` | This is a **path ledger**, not a transaction. Finish does NOT freeze. No rollback. |
| ` + "`ROLLBACK`" + ` | ` + "`box_task_abort`" + ` (appends a ✗ event) | No state reversion. The ✗ event becomes part of the history. |
| ` + "`UPDATE`" + ` | ` + "`box_replace_item`" + ` (new revision) or ` + "`box_set_item_symbols`" + ` (overwrite 觉痕) | Replace opens a new revision; the old one is kept. Status flip mutates in place. |
| Primary key | ` + "`item_id`" + ` (server-issued) + ` + "`idem_key`" + ` (caller-issued) | Idempotency at the protocol level, no upsert. |
| Schema migration | None. ` + "`symbols`" + ` are open-set; ` + "`labels`" + ` are free-form. | Drop in new symbol kinds without DDL. |
| ACL / GRANT | Two-layer: (1) Bearer token gates HTTP + MCP entry. (2) On WRITES, ` + "`caller_id`" + ` must equal ` + "`box.owner_id`" + ` — see the auth section below for the exact rule. Reads are not caller-scoped. | Single-tenant + one hidden write gate. See "Auth: two layers" below. |
| Full-text search | **Not built in.** ` + "`box_browse`" + ` does exact-label match only. | Roadmap R2.1/R2.2; today, do retrieval in your agent before calling Box. |

## Common anti-patterns (do not do these)

1. **"Let me query by content."** — ` + "`box_browse`" + ` does not scan
   content. Tag items with topic/scope symbols at store time, then
   ` + "`box_trace --kind=topic`" + ` later.
2. **"I'll send box_task_finish a pass/fail and Box will check."** — Box
   stores pass_criteria; the **agent** executes it. Run your own check,
   then call ` + "`box_task_finish`" + ` with the verdict.
3. **"I'll batch insert."** — There is no bulk endpoint yet (R0.14
   backlog). Today: loop ` + "`box_store`" + ` per item.
4. **"The task is done, so it's locked."** — No. 合程 is an append, not a
   freeze. ` + "`box_set_item_symbols`" + ` can flip ✓ back to → days
   later. This is invariant #12 and it is intentional.
5. **"Token is who I am."** — Token is just a session handle for one
   in-flight 程辙. It is NOT identity. Process restart wipes all tokens.
6. **"I'll subscribe to changes."** — No item-level watch yet (R0.16
   backlog). MCP's ` + "`listChanged`" + ` is for tool metadata, not data.

## Five concepts to know

1. **Box** = a named container. Created with ` + "`box_create_box`" + `.
2. **Item** = a typed row inside a box. Stored with ` + "`box_store`" + `.
3. **Symbol** = ` + "`{kind, value, ref?}`" + `. Drives ` + "`box_trace`" + `,
   ` + "`box_neighbors`" + `, and status flips. **This is the primary
   lookup mechanism, not labels.**
4. **Task** = an item with kind=task carrying intent/goal/pass_criteria/
   nail_chain. Created with ` + "`box_task_start`" + ` (which makes the
   task item and opens a 程辙 session in one call).
5. **程辙 / YiCheng** = a session-scoped task lifecycle. Open with
   ` + "`box_task_start`" + ` (returns a token), append events with
   ` + "`box_append_event`" + `, close with ` + "`box_task_finish`" + `
   or ` + "`box_task_abort`" + `. **Path ledger, not a transaction.** 合程
   (finish) is one more append; it does NOT freeze the task.

## Three invariants (read these once, internalize forever)

- **#10 Box is dumb storage.** Schema validation only; never interprets
  ` + "`pass_criteria.query`" + `. That's the agent's job.
- **#11 Token = session, not authorization.** ` + "`tsk_…`" + ` tokens
  identify a live 程辙 session in memory. They are NOT ACLs.
- **#12 Box is 符径 (Symbol Path), not a database.** Append-only path
  ledger. 觉痕 (awareness markers: ✓ / ✗ / ? / → / ~ / ◯) can be overwritten
  by subsequent events.

## Auth: zero-token on your tailnet (R7)

If box-mcp runs with ` + "`--trust-tailnet`" + ` (or ` + "`BOX_TRUST_TAILNET=1`" + `),
any request whose source IP is on the Tailscale tailnet
(` + "`100.64.0.0/10`" + ` or ` + "`fd7a:115c:a1e0::/48`" + `) skips the Bearer check
entirely — Tailscale already authenticated the device when you logged into
your account and authorised it. **Inside your tailnet you need no token at
all.** Public (non-tailnet) requests still require the Bearer token, so a
single deployment serves tailnet agents token-free with a public fallback.

Do NOT enable trust-tailnet behind an L7 proxy that rewrites the source IP
(e.g. Fly's edge) — the peer there is the proxy, not your device. Fly stays
Bearer-only; your Mac/tailnet box-mcp runs --trust-tailnet.

## Auth: two layers (do not skip this)

box-mcp has **two** authorization layers; missing the second is the
single most common stumble for fresh agents.

### Layer 1 — Bearer token

Every HTTP request needs ` + "`Authorization: Bearer <BOX_API_TOKEN>`" + `. No token
→ ` + "`401 Unauthorized`" + `. Wrong token → also ` + "`401`" + ` (constant-time compare).

### Layer 2 — caller / owner gate (writes only)

When you ` + "`box_create_box`" + ` you set ` + "`owner_id`" + ` (any string you want).
On every WRITE to that box — store / replace / labels / delete / consume /
set_item_symbols / append_event — the server checks
` + "`caller_id == box.owner_id`" + `. If they differ you get:

` + "```" + `
forbidden: caller_owner_mismatch caller="<your-caller>" box_owner="<the-box-owner>"
` + "```" + `

` + "`caller_id`" + ` is the server-side default from ` + "`--owner`" + ` flag or
` + "`$BOX_CALLER`" + ` env (see ` + "`cmd/box-mcp/main.go:resolveCaller`" + `). For a remote
deployment it is whatever the operator configured at startup. You cannot
choose it per-request and there is **no whoami endpoint** (R0.23 noted; F2
deferred).

### Practical rule

When you create a new box, set ` + "`owner_id`" + ` to the deploying caller's id
(typically ` + "`trine`" + ` on this deployment). Reads on any box work
regardless. If a write returns ` + "`forbidden: caller_owner_mismatch`" + ` and you
own the deployment, the cheapest fix is recreate-or-replace the box with
the matching ` + "`owner_id`" + ` — not rotate the token.

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

## The 28 tools (plus HTTP-only routes — see "Uploading & downloading files" below)

### Box / Item CRUD (18)
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
- ` + "`box_overview`" + ` — cross-box geo-globe view (axis × zoom × filter, caller-scoped; R5)

### Task surface (R0.10) (5)
- ` + "`box_set_item_symbols`" + ` — replace any item's symbol set (covers status flip; was box_set_task_status pre-R0.13.2)
- ` + "`box_append_event`" + ` — append a TraceStep to any item (no kind=task gate)
- ` + "`box_list_events`" + ` — full event history for any item

Task items use ` + "`box_task_start`" + ` (creates kind=task + opens session)
and ` + "`box_show`" + ` for reads. ` + "`box_create_task`" + `, ` + "`box_get_task`" + `
and ` + "`box_set_task_status`" + ` were removed in R0.13.2 — they were
just kind=task-flavoured aliases of box_store / box_show /
box_set_item_symbols.

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

### Blob consistency audit (R0.19) (1)
- ` + "`box_gc_blobs`" + ` — scan items vs disk blobs; report orphans
  (delete-candidates) and missing refs (alerts). Default ` + "`dry_run=true`" + `;
  pass ` + "`{\"dry_run\":false}`" + ` to actually delete orphan blobs older
  than 24 h.

### Sphere navigation (R6) (2)
- ` + "`box_set_box_labels`" + ` — write Labels on a Box (` + "`mode=merge`" + ` default;
  ` + "`replace`" + ` overwrites). Empty value deletes the key. Caller==owner gate.
- ` + "`box_globes`" + ` — multi-globe view: groups caller-owned boxes by
  ` + "`Labels[\"__op:sphere\"]`" + ` (override via ` + "`sphere_label`" + ` arg).
  Returns per-sphere BoxGlyph list + ItemCount + CountsByKind aggregate;
  unassigned bucket holds boxes with no sphere label. Sorted alphabetically.

The "sphere" is a convention — a logical grouping of boxes (department,
project area, personal vs work, …). Set it on creation via
` + "`box_create_box {labels: {\"__op:sphere\": \"engineering\"}}`" + ` or
retroactively via ` + "`box_set_box_labels`" + `. Drill into one sphere via
` + "`box_overview {axis:\"...\", filter:{labels:{\"__op:sphere\":\"engineering\"}}}`" + `.

## Uploading & downloading files (R0.18 + R0.19)

Bytes do NOT go through MCP tools. Item content over JSON-RPC is base64 +
33% overhead + whole-file in one message — fine for small markdown, awful
for binaries. Instead box-mcp mounts **plain HTTP routes** alongside the
MCP transport. **These routes only exist in HTTP mode** (` + "`--http=:8080`" + `
or ` + "`$PORT`" + `); stdio-only deployments cannot upload/download bytes.

### Routes (all Bearer-protected)

| Route | Use |
| --- | --- |
| ` + "`POST /blob/upload`" + ` | Stream raw bytes in the request body. Server hashes (sha256), stores, returns ` + "`{sha256, size, deduped, storage_uri}`" + `. Idempotent: same bytes → same sha → ` + "`deduped:true`" + ` on retry. |
| ` + "`GET /blob/<sha256>`" + ` | Stream blob bytes back. Supports HTTP Range (resume safe) and ETag. |
| ` + "`HEAD /blob/<sha256>`" + ` | Exists check; cheap pre-flight before re-upload. |
| ` + "`GET /items/<item_id>/blob`" + ` | **Gold-path download.** Server resolves the item → parses ` + "`storage_uri`" + ` → streams the blob. Use this when an external machine holds only an item id. Returns ` + "`ETag`" + `, ` + "`X-Box-Sha256`" + `, ` + "`X-Box-Format`" + `, ` + "`X-Box-Item-ID`" + ` headers. |
| ` + "`GET /healthz`" + ` | No auth; 200 ok. For tunnel / loadbalancer probes. |

### Canonical upload flow

` + "```bash" + `
# 1. Upload bytes → get sha
RESP=$(curl -sS -X POST "$URL/blob/upload" \
  -H "Authorization: Bearer $TOKEN" --data-binary @local-file.pdf)
SHA=$(echo "$RESP" | jq -r .sha256)

# 2. Register the item (metadata only; bytes already on the server)
# Pre-req: box's storage_policy allows the format AND max_content_bytes
# does not block (inline content here is just JSON metadata, not the bytes).
curl -sS -X POST "$URL/mcp" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"box_store","arguments":{
        "box_id":"<box-id>",
        "kind":"A",
        "source_type":"upload",
        "storage_uri":"blob://sha256/'"$SHA"'",
        "format":"binary",
        "idem_key":"upload::local-file.pdf",
        "content":{"name":"local-file.pdf","mime":"application/pdf"},
        "symbols":[{"kind":"kind","value":"A"}]
      }}}'
# → captures item_id from response
` + "```" + `

### Canonical download flow (any machine with the item_id)

` + "```bash" + `
curl -sS "$URL/items/<item_id>/blob" \
  -H "Authorization: Bearer $TOKEN" \
  -o downloaded-file
# Headers include ETag = sha256 — verify with shasum -a 256 if paranoid.
# Range supported: -H "Range: bytes=0-1023" for partial.
` + "```" + `

### Two hashes, do not confuse them

` + "`box_store`" + ` returns an Item whose body contains ` + "`content_hash`" + `. This is
the sha256 of the **JSON metadata payload you sent in ` + "`content`" + `**, not the
sha of the underlying bytes. The blob layer's sha (returned by
` + "`/blob/upload`" + `) is a separate value covering the actual file bytes.

| Field | What it hashes | Where you see it |
| --- | --- | --- |
| ` + "`item.content_hash`" + ` | The metadata JSON in ` + "`item.Content`" + ` (e.g. ` + "`{\"name\":\"x.pdf\"}`" + `) | ` + "`box_store`" + ` response, ` + "`box_show`" + ` response |
| blob sha256 | The raw bytes you POSTed to ` + "`/blob/upload`" + ` | ` + "`/blob/upload`" + ` response, ETag on GETs, ` + "`X-Box-Sha256`" + ` header, the ` + "`<sha>`" + ` in ` + "`storage_uri: blob://sha256/<sha>`" + ` |

Round-trip verification for the upload uses the **blob sha**, not
` + "`content_hash`" + `. If you store the same bytes twice with different metadata
JSON, the blob sha is identical (dedup); the ` + "`content_hash`" + ` of the two
items differs because the metadata differs.

### Format whitelist vs storage_uri scheme (independent gates)

These are two unrelated checks; agents sometimes confuse them.

- **` + "`item.format`" + `** (` + "`json`" + `, ` + "`markdown`" + `, ` + "`text`" + `, ` + "`binary`" + `, ` + "`pdf`" + `, ` + "`png`" + ` …)
  — must appear in the box's ` + "`storage_policy.allowed_formats`" + ` list. The
  default policy is ` + "`[\"json\",\"markdown\",\"text\"]`" + `; create the box with
  ` + "`storage_policy.allowed_formats:[\"binary\",\"pdf\",…]`" + ` to admit other
  formats. Validation is a string-set check on the value of ` + "`item.format`" + `.

- **` + "`item.storage_uri`" + ` scheme** (` + "`row://`" + `, ` + "`blob://sha256/`" + `, ` + "`s3://`" + `,
  ` + "`folder://`" + `, ` + "`repo://`" + `, ` + "`ipfs://`" + `, ` + "`collection://`" + `) — must be one of
  these seven prefixes. This says where the bytes LIVE; the format is what
  they ARE. Box validates the prefix but never dereferences the URI
  (invariant #10).

A typical blob-backed PDF item would set ` + "`format: \"pdf\"`" + ` AND ` + "`storage_uri:\n  \"blob://sha256/<sha>\"`" + ` — both checks must pass. Make sure your box's
` + "`allowed_formats`" + ` includes ` + "`\"pdf\"`" + ` (or whatever you choose) when you
create it.

### Atomicity & retry semantics

The upload and ` + "`box_store`" + ` are **two writes, not a transaction**. But the
flow is **one-way safe**: sha is only returned AFTER the blob is on disk, so
items cannot reference missing blobs (no dangling pointer is reachable
through the normal flow). The only failure mode is *orphan blob* — bytes
written, ` + "`box_store`" + ` didn't follow (network blip, crash). These cost
disk only; ` + "`box_gc_blobs`" + ` reclaims them after 24 h. Concretely:

- **Client retry policy**: blob_put is idempotent via sha (content-addressed);
  box_store is idempotent via ` + "`idem_key`" + `. Retry both freely.
- **Server invariants**: ` + "`/items/<id>/blob`" + ` returns ` + "`502`" + ` if the item
  references a sha not on disk — alert the operator and run ` + "`box_gc_blobs`" + `
  (the report's ` + "`missing[]`" + ` field lists every broken ref).

## Minimal happy path (copy/paste-able)

` + "```json" + `
// 1. create box
// owner_id MUST equal your caller identity (the one bound to your Bearer
// token; see "Auth: two layers" above). Substitute it before pasting.
{"tool":"box_create_box","args":{"key":"my-km","owner_type":"user","owner_id":"<your-caller-id>"}}

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
{"tool":"box_append_event","args":{
  "item_id":"item_…",
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

## Capability matrix (what box-model does NOT do, and why)

### By design — these are not coming

| Capability | Why we refuse |
| --- | --- |
| Multi-user ACL / RBAC | One-person company. Bearer token = full tenant. Invariant #11. |
| Box executes ` + "`pass_criteria.query`" + ` | Invariant #10. Verdict is agent work. Box only validates the schema. |
| Built-in embedding / RAG pipeline | Avoids model lock-in. Box stores ` + "`storage_uri`" + ` and (optional) chunk hashes; agents run their own embedding / retrieval. |
| Web UI | Pure agent interface. Humans use ` + "`box view / rotate`" + ` CLI; no browser UI is planned. |

### On roadmap — real gaps with R numbers

| Capability | Status | Workaround today |
| --- | --- | --- |
| Semantic / vector recall | **R2.1** (future) | Do retrieval in your agent; pass result IDs to ` + "`box_browse`" + `. |
| BM25 / keyword full-text | **R2.2** (future) | Same — agent-side. |
| Bulk ingest (multi-item per RPC) | **R0.14** (backlog) | Loop ` + "`box_store`" + ` per item. ~10 ms per call locally. |
| Query predicates (range, compound) | **R0.15** (backlog) | Combine ` + "`box_trace`" + ` + client-side filter. |
| Item change subscription / watch | **R0.16** (backlog) | Poll ` + "`box_browse`" + ` or ` + "`box_list_events`" + `. |
| Multi-tenant / per-agent token scope | **R4.2 v2** (future) | Single ` + "`BOX_API_TOKEN`" + ` only. |

## Pitfalls (small but bite often)

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
- Process restart invalidates all live tokens (invariant #11). If you held
  a token across a restart, ` + "`FinishYiCheng`" + ` will refuse; flip 觉痕
  manually via ` + "`box_set_item_symbols`" + ` instead.

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
		Version: "0.19.0",
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
