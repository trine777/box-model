# Box Model MCP Server

`box-mcp` exposes the Box Model `Service` façade as an MCP (Model Context
Protocol) server over stdio. LLM clients (Claude Desktop, mcp-cli, etc.) can
spawn it as a child process and call the 17 exposed Box tools directly.

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

## Exposed tools (17)

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

Each tool's input schema is auto-derived from its typed Go struct in
`cmd/box-mcp/main.go`; clients receive it via `tools/list`.

## Not exposed

Human-facing surfaces are intentionally omitted to keep tool responses
machine-friendly JSON:

- `box view` / `box rotate` — human-facing renderings (ASCII / mermaid).
- `box_legend_all`, `box_list_boxes` — deferred to R0.8.2.
- `import-nailforge` — data-migration tool, not an LLM verb.

## Errors

`box.Service` returns `ErrValidation` / `ErrForbidden` / `ErrNotFound` /
`ErrConflict`. The handler returns the error as-is; the SDK turns it into a
`CallToolResult` with `IsError: true` and a TextContent block whose text
preserves the wrapped sentinel prefix (e.g. `validation: key is required`).
Clients can parse the prefix to recover the error class.
