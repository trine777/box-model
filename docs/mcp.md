# Box Model MCP Server

`box-mcp` exposes the Box Model `Service` façade as an MCP (Model Context
Protocol) server. Supports two transports:
- **stdio** (default) — child-process JSON-RPC for Claude Desktop / Claude Code.
- **HTTP** (`--http=:8080`, `BOX_API_TOKEN` required) — Streamable-HTTP for
  remote agents; mounts `/mcp`, `/blob/*`, `/items/<id>/blob`, `/healthz`.

For step-by-step setup of either transport (Claude wiring, Fly.io deploy,
tunnels, blob upload, GC scheduling, env vars, troubleshooting) see
[**docs/configuration.md**](./configuration.md). This file is the
tool-surface reference only.

## Spawn

    box-mcp --owner=trine
    # BOX_HOME=~/.box (default), override with --box-home or $BOX_HOME

Flags:

- `--owner=ID`     Default `caller_id` for every tool call. Falls back to
                   `$BOX_CALLER`, then to the resolved box owner.
- `--box-home=DIR` Override the storage root (else `$BOX_HOME`, else `~/.box`).
- `--no-obs`       Disable observability (the MemObserver). Mainly for tests.

## Connect from Claude Desktop / mcp-cli

    {
      "command": "/path/to/box-mcp",
      "args": ["--owner=trine"],
      "env": {"BOX_HOME": "/path/to/.box"}
    }

## Exposed tools (28)

| Tool | Service method |
| --- | --- |
| `box_create_box`     | `Service.CreateBox` |
| `box_get_box_by_key` | `Service.GetBoxByKey` |
| `box_seal_box`       | `Service.SealBox` |
| `box_summary`        | `Service.Summary` |
| `box_store`          | `Service.Store` |
| `box_replace_item`   | `Service.ReplaceItem` |
| `box_update_labels`  | `Service.UpdateLabels` |
| `box_merge_labels`   | `Service.MergeLabels` |
| `box_remove_labels`  | `Service.RemoveLabels` |
| `box_delete_item`    | `Service.DeleteItem` |
| `box_consume`        | `Service.Consume` |
| `box_show`           | `Service.GetItem` |
| `box_browse`         | `Service.Browse` |
| `box_list_consumes`  | `Service.ListConsumes` |
| `box_trace`          | `Service.Trace` |
| `box_legend`         | `Service.LegendOf` |
| `box_neighbors`      | `Service.Neighbors` |
| `box_overview`       | `Service.Overview` — R5.1 geo-globe view over all caller-owned boxes (axis × zoom × filter). |
| `box_set_item_symbols` | `Service.SetItemSymbols` — replace any item's symbol set; supersedes the deprecated kind=task-specific `box_set_task_status`. |
| `box_append_event`   | `Service.AppendEvent` (R0.13.2) — append one TraceStep to any item's `trace.jsonl`. Works on any kind. |
| `box_list_events`    | `Service.ListEvents` — read an item's full event history. |
| `box_task_start` / `box_task_finish` / `box_task_abort` / `box_task_token_status` | `Service.StartYiCheng` / `FinishYiCheng` / `AbortYiCheng` / `ValidateYiCheng` (R0.13.1 程辙 layer). |
| `box_manual` / `box_legend_all` | Self-describing (R4.1): full traffic manual + 25 native symbol legend, for cold-start agents. |
| `box_gc_blobs`       | R0.19 blob consistency audit — orphan sweep + missing-ref alert. Default `dry_run=true`. |

Each tool's input schema is auto-derived from its typed Go struct in
`cmd/box-mcp/main.go`; clients receive it via `tools/list`.

### HTTP-only routes (not MCP tools)

Mounted alongside `/mcp` when running in `--http` mode. Same Bearer auth.

| Route | Purpose |
| --- | --- |
| `POST /blob/upload` | stream bytes → server hashes + dedups → returns `{sha256, size, storage_uri}` |
| `GET /blob/<sha256>` | stream bytes back (Range + ETag) |
| `HEAD /blob/<sha256>` | exists check |
| `GET /items/<item_id>/blob` | one-shot: lookup item → parse `storage_uri` → stream blob (the download path for external machines holding only an item id) |
| `GET /healthz` | no-auth liveness probe |

These deliberately don't go through MCP because byte streams and Range
requests belong in HTTP, not JSON-RPC.

## Not exposed

Human-facing surfaces are intentionally omitted to keep tool responses
machine-friendly JSON:

- `box view` / `box rotate` — human-facing renderings (ASCII / mermaid).
- `box_list_boxes` — replaced by `box_overview` (R5.1, caller-scoped,
  axis × zoom).
- `import-nailforge` — data-migration tool, not an LLM verb.

`box_create_task` / `box_get_task` / `box_set_task_status` were removed in
R0.13.2 — they were kind=task-flavoured aliases that broke invariant #10.
Use `box_task_start` (creates kind=task + opens 程辙 session in one call),
`box_show`, and `box_set_item_symbols` respectively.

## Errors

`box.Service` returns `ErrValidation` / `ErrForbidden` / `ErrNotFound` /
`ErrConflict`. The handler returns the error as-is; the SDK turns it into a
`CallToolResult` with `IsError: true` and a TextContent block whose text
preserves the wrapped sentinel prefix (e.g. `validation: key is required`).
Clients can parse the prefix to recover the error class.
