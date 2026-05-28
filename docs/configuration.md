# Configuration Guide

How to wire up `box-mcp` for the four common scenarios, what env vars exist,
and how to keep the data layer healthy. Newcomer agents: read this in order;
each section assumes the previous ran cleanly.

## TL;DR — pick a mode

| Your situation | Mode | Section |
| --- | --- | --- |
| Claude Desktop / Claude Code on your Mac, just you | **Mode 1 — local stdio** | [§1](#mode-1--local-stdio) |
| Mac is the source of truth, other agents (on phones / VMs / cloud) need to reach it | **Mode 2 — local HTTP + tunnel** | [§2](#mode-2--local-http--tunnel) |
| Always-on public endpoint, low-traffic | **Mode 3 — Fly.io deploy** | [§3](#mode-3--flyio-deploy) |
| All of the above (Mac main + Fly metadata replica) | mix and match | [§5](#5-multi-host-considerations) |

If you only want to read items: every mode supports the same MCP tool surface
(28 tools as of R0.19). Only the transport and the storage location change.

---

## Mode 1 · local stdio

The default. Box stores data under `~/.box/`, Claude spawns `box-mcp` as a
child process and talks JSON-RPC over the spawned stdin/stdout. **No
network. No Bearer token.**

### Install

```bash
git clone https://github.com/trine777/box-model
cd box-model
go build -o ~/.local/bin/box-mcp ./cmd/box-mcp
```

### Wire to Claude Code

```bash
claude mcp add -s user box ~/.local/bin/box-mcp \
  -e BOX_HOME=$HOME/.box -e BOX_CALLER=$(whoami)
```

Confirm:

```bash
claude mcp get box        # should print "✓ Connected"
```

In a fresh Claude Code session (start a new one — MCP servers do not hot
reload) the box tools appear as `mcp__box__box_*`. Quick test:

```
Call mcp__box__box_manual
```

Returns the ~10 KB self-describing manual (`box_manual` is tool #28).

### Wire to Claude Desktop

Edit `~/Library/Application Support/Claude/claude_desktop_config.json` —
**merge with existing keys, do not overwrite**:

```json
{
  "preferences": { /* keep whatever was here */ },
  "mcpServers": {
    "box": {
      "command": "/Users/<you>/.local/bin/box-mcp",
      "env": {
        "BOX_HOME": "/Users/<you>/.box",
        "BOX_CALLER": "<you>"
      }
    }
  }
}
```

Restart Claude Desktop (`cmd+Q`, reopen — close-window isn't enough).

---

## Mode 2 · local HTTP + tunnel

Mac becomes the storage server. External agents call HTTPS endpoints; bytes
land on your disk. Required when other machines need to read/write the same
`~/.box/`.

### Step 1 — start box-mcp in HTTP mode

```bash
TOKEN=$(openssl rand -hex 32)
echo "$TOKEN" > ~/.box-api-token        # save for clients
chmod 600 ~/.box-api-token

BOX_HOME=$HOME/.box \
BOX_CALLER=$(whoami) \
BOX_API_TOKEN=$TOKEN \
caffeinate -i ~/.local/bin/box-mcp --http=:8080
```

`caffeinate -i` blocks system sleep while box-mcp runs — without it the Mac
naps and external agents lose access. For an autostart service use launchd
(see [§6](#6-launchd-mac-autostart)).

The server listens on **three** HTTP route families, all behind the same
Bearer middleware:

| Route | Purpose |
| --- | --- |
| `POST /mcp` | Streamable-HTTP MCP for all 28 tools |
| `POST /blob/upload`, `GET/HEAD /blob/<sha256>` | direct byte upload/download |
| `GET /items/<item_id>/blob` | one-shot: lookup item + stream its referenced blob |

Plus `GET /healthz` (no auth) for liveness probes.

### Step 2 — expose to the internet (pick one)

**Cloudflare Tunnel (recommended — free, no signup for trycloudflare URLs)**

```bash
brew install cloudflared
cloudflared tunnel --url http://localhost:8080
# prints: https://<random-words>.trycloudflare.com
```

That URL is your public endpoint. Tunnel persists for the lifetime of the
`cloudflared` process; if you want a stable hostname, run `cloudflared
tunnel login` once and set up a named tunnel.

**Tailscale (no public exposure, only your devices)**

```bash
brew install --cask tailscale
tailscale up
tailscale ip -4              # → 100.x.y.z
```

Other Tailscale-meshed devices can reach `http://100.x.y.z:8080/mcp`
directly. Stays on your private mesh.

**ngrok (quick testing)**

```bash
ngrok http 8080              # prints https://*.ngrok-free.app
```

### Step 3 — verify from another machine

```bash
URL=https://<your-tunnel>            # or http://100.x.y.z:8080 for Tailscale
TOKEN=$(cat ~/.box-api-token)        # or paste from §1

curl -sf "$URL/healthz"                                                 # 200
curl -sS "$URL/mcp" -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"probe","version":"0.1"}}}'
```

The second curl should return an SSE `data:` line with `serverInfo`.

### Step 4 — upload a file, store metadata, download from elsewhere

```bash
# Upload bytes (sha256 hashing + dedup happens server-side)
RESP=$(curl -sS -X POST "$URL/blob/upload" \
  -H "Authorization: Bearer $TOKEN" \
  --data-binary @./big-video.mp4)
echo "$RESP"
# {"sha256":"…","size":…,"deduped":false,"storage_uri":"blob://sha256/…"}
SHA=$(echo "$RESP" | jq -r .sha256)

# Create the item (one-time setup of the box; subsequent uploads reuse it)
# Storage policy lets you raise/remove the 256KB inline-content cap and
# whitelist binary formats — see §4.
curl -sS "$URL/mcp" -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"box_store\",\"arguments\":{
        \"box_id\":\"<your_box_id>\",
        \"kind\":\"A\",
        \"source_type\":\"upload\",
        \"storage_uri\":\"blob://sha256/$SHA\",
        \"format\":\"binary\",
        \"idem_key\":\"upload::big-video.mp4\",
        \"content\":{\"name\":\"big-video.mp4\",\"mime\":\"video/mp4\"},
        \"symbols\":[{\"kind\":\"kind\",\"value\":\"A\"}]
      }}}"
# returns the item, capture its id (the "id" field)

# Download from any other machine, given just the item_id:
curl -sS "$URL/items/<item_id>/blob" -H "Authorization: Bearer $TOKEN" -o local-copy.mp4
```

`/items/<id>/blob` is the **gold-path download** — external agents who hold
only an item id can fetch bytes in one HTTP call. It returns:

- `ETag: "<sha256>"` (clients can cache-validate)
- `X-Box-Sha256`, `X-Box-Item-ID`, `X-Box-Format` headers for verification
- Range requests work (`-H "Range: bytes=0-1023"`) — resume safe for big
  files

---

## Mode 3 · Fly.io deploy

Always-on, public HTTPS endpoint, your home IP isn't involved. Best for
metadata + smallish blobs (Fly's volume is finite — 1 GB on the free tier).

### One-time

```bash
fly apps create box-mcp-<your-handle> --org personal
fly volumes create box_data --size 1 --region nrt -a box-mcp-<your-handle>
fly secrets set BOX_API_TOKEN=$(openssl rand -hex 32) -a box-mcp-<your-handle>
fly deploy -a box-mcp-<your-handle>
```

The included `Dockerfile` and `fly.toml` are pre-wired:

- Mount: `box_data` → `/data`
- Env: `BOX_HOME=/data`, `BOX_BLOB_ROOT=/data/blobs`
- Region: `nrt` (Tokyo). Change `primary_region` in `fly.toml` if you want
  closer/elsewhere.
- Auto-stop: the VM idles to 0 when no traffic. First request from a cold
  start takes ~3 s. For latency-sensitive use, set
  `auto_stop_machines = false` in `fly.toml`.

### Verify

```bash
curl -sf https://box-mcp-<handle>.fly.dev/healthz   # 200 ok

curl -sS https://box-mcp-<handle>.fly.dev/mcp \
  -H "Authorization: Bearer $(fly secrets list -a box-mcp-<handle> | grep BOX_API_TOKEN | awk '{print $3}')" \
  -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"probe","version":"0.1"}}}'
```

Read the token once and store it locally (`fly secrets list` does not echo
secret values — you have to remember from when you set them, or rotate via
`fly secrets set BOX_API_TOKEN=<new>` + redeploy).

---

## 4 · Storage policy per box

Every box has a `storage_policy` that controls inline content. Defaults:

```json
{
  "allowed_formats": ["json", "markdown", "text"],
  "max_items": 1000,
  "max_content_bytes": 262144
}
```

Three knobs in plain language:

| Field | What it caps | When to raise |
| --- | --- | --- |
| `allowed_formats` | Which `Item.Format` values pass validation | Storing binaries, PDFs, images, etc. |
| `max_items` | Items per box (closed-set quota) | Archive-style boxes with > 1000 entries |
| `max_content_bytes` | Bytes per `item.Content` (the inline JSON field) | Inline content larger than 256 KB; set to `0` for unlimited |

For a "media archive" box where actual bytes live in the blob layer and the
item only stores metadata:

```json
{
  "key": "media-archive",
  "owner_id": "trine",
  "storage_policy": {
    "allowed_formats": ["binary", "pdf", "png", "mp4", "markdown", "json"],
    "max_items": 100000,
    "max_content_bytes": 0
  }
}
```

`max_content_bytes: 0` means "no inline content cap" — useful when you push
bytes through `/blob/upload` and only the pointer (`storage_uri =
"blob://sha256/..."`) sits in the item.

---

## 5 · Multi-host considerations

If you run **Mac + Fly simultaneously**, they are two independent stores.
There is no built-in replication. You will get two unrelated copies of every
box and item unless you pick a discipline:

| Pattern | Mac role | Fly role |
| --- | --- | --- |
| **All on Mac** (recommended for one-person) | source of truth | nothing |
| **All on Fly** | nothing | source of truth, public |
| **Split** | private high-volume blob store | small metadata index, public |

If you go "split", note that storage_uri values written from Fly point at
`blob://sha256/<sha>` — Fly's blob layer, not Mac's. Cross-host blob fetch
would need an explicit URL scheme (`http://other-host/blob/...`) and Box
doesn't dereference URIs (invariant #10), so the agent is responsible for
knowing where to fetch.

For now, **pick one source of truth**. Two-way sync is roadmap.

---

## 6 · launchd (Mac autostart)

To keep box-mcp HTTP running on boot under your user account:

```bash
mkdir -p ~/Library/LaunchAgents
cat > ~/Library/LaunchAgents/com.box-mcp.plist <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>             <string>com.box-mcp</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/YOU/.local/bin/box-mcp</string>
    <string>--http=:8080</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>BOX_HOME</key>        <string>/Users/YOU/.box</string>
    <key>BOX_CALLER</key>      <string>YOU</string>
    <key>BOX_API_TOKEN</key>   <string>PUT-YOUR-TOKEN-HERE</string>
  </dict>
  <key>RunAtLoad</key>         <true/>
  <key>KeepAlive</key>         <true/>
  <key>StandardOutPath</key>   <string>/tmp/box-mcp.out.log</string>
  <key>StandardErrorPath</key> <string>/tmp/box-mcp.err.log</string>
</dict>
</plist>
EOF

launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.box-mcp.plist
launchctl print gui/$(id -u)/com.box-mcp | head -20    # confirm running
```

Remove with `launchctl bootout gui/$(id -u)/com.box-mcp`.

---

## 7 · Data consistency (GC)

The blob layer (R0.18) and item metadata (box core) are two independent
stores. Their writes happen sequentially:

1. `POST /blob/upload` → bytes on disk, server returns `sha256`
2. agent calls `box_store` with `storage_uri="blob://sha256/<sha>"`

Step 1 always precedes Step 2, so items never reference missing blobs
through the normal flow. The only failure mode is **orphan blobs** — bytes
written but the follow-up `box_store` never landed (network blip, agent
crashed, etc.). Disk waste, not data loss.

### Run GC

Default dry-run (safe, just reports):

```
Call mcp__box__box_gc_blobs   (from Claude)
```

or via curl:

```bash
curl -sS "$URL/mcp" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"box_gc_blobs","arguments":{"dry_run":true}}}'
```

Output:

```json
{
  "blob_root": "/data/blobs",
  "disk_blobs": 42,
  "ref_blobs": 38,
  "orphans": ["aa4eff03...", "..."],
  "missing": [],
  "dry_run": true,
  "older_than_sec": 86400
}
```

Read the report:

- `orphans` (disk has, no item refs) — delete candidates. **Spared if newer
  than 24 h** (probably in-flight upload).
- `missing` (item references it, no on-disk blob) — **alert**. Box never
  auto-fixes; manual investigation needed (someone deleted blob files? did
  you switch to a different blob root?). Use `box_show <item_id>` on each
  to investigate.

To actually delete orphans:

```bash
# add "dry_run": false
... -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"box_gc_blobs","arguments":{"dry_run":false}}}'
```

### Schedule GC

Once a day is fine for most workloads:

```bash
# crontab -e
0 3 * * * curl -sS http://localhost:8080/mcp \
  -H "Authorization: Bearer $(cat ~/.box-api-token)" \
  -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"box_gc_blobs","arguments":{"dry_run":false}}}' \
  >> ~/.box/gc.log 2>&1
```

If you want a stricter window (delete orphans after 1 hour instead of 24):

```json
{"dry_run":false,"older_than_seconds":3600}
```

---

## 8 · Environment variables reference

| Variable | Default | What it does |
| --- | --- | --- |
| `BOX_HOME` | `~/.box` | Storage root. Items live under `<root>/boxes/<key>/items/`. |
| `BOX_CALLER` | (none) | Default `caller_id` for tool calls that don't supply one. |
| `BOX_API_TOKEN` | (none — required for `--http`) | Bearer token clients must send as `Authorization: Bearer <token>`. |
| `BOX_BLOB_ROOT` | `$BOX_HOME/blobs` | Override where blob files land. Useful if blobs and items live on different volumes. |
| `BOX_OBS_DISABLED` | `false` | Set `1` / `true` to disable the MemObserver (counters + slog logs). |
| `BOX_LOG_PATH` | stderr | If set, slog JSON lines go to this file instead of stderr. |
| `BOX_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error`. |
| `PORT` | (none) | If set and `--http` is not, box-mcp serves HTTP on `:$PORT`. Fly sets this. |

CLI flags (override env where overlapping):

| Flag | Env equivalent |
| --- | --- |
| `--box-home=DIR` | `$BOX_HOME` |
| `--owner=ID` | `$BOX_CALLER` |
| `--http=:8080` | `$PORT` |
| `--no-obs` | `BOX_OBS_DISABLED=1` |

---

## 9 · Bearer token management

- Generate: `openssl rand -hex 32` — 256 bits of entropy, fits one line.
- Storage: never commit. Use `chmod 600 ~/.box-api-token` for local
  storage; use `fly secrets set` for Fly.
- Rotate: pick a new token, push it (`fly secrets set BOX_API_TOKEN=...`),
  restart any local server, **then** update every client.
- Single-tenant by design: one token = full access to the entire tenant
  (every box you own). No per-box scoping. Invariant #11.

For multi-client deployments where some clients should be read-only:
**there is no built-in solution** (single-tenant). Workaround: run two
separate `box-mcp` processes against the same `BOX_HOME`, one with
`BOX_API_TOKEN=write-key` and `BOX_OBS_DISABLED=…` (no policy difference)
and another with a read-only proxy you build yourself. Roadmap candidate.

---

## 10 · Troubleshooting

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| `claude mcp get box` says ✗ Failed | binary path wrong, or BOX_HOME unwritable | `ls -l ~/.local/bin/box-mcp`; `ls -ld ~/.box` (should be writable by you) |
| Claude Code "tools fetch failed" with `box ·  △ connected` | (was a known R4.1 bug; fixed in R4.2 `14e9bfb`) | `git pull && go build -o ~/.local/bin/box-mcp ./cmd/box-mcp`, restart Claude |
| `GET /items/<id>/blob` → 502 | item references a sha not on disk | `box_gc_blobs` and look at `missing` |
| `box_store` returns `validation: content size ... exceeds max` | inline content > `max_content_bytes` | Raise the policy or move bytes into `/blob/upload` + use `blob://` storage_uri |
| Upload returns `validation: unsupported storage_uri scheme` | trying to write a scheme other than `row://`, `blob://`, `s3://`, `folder://`, `repo://`, `ipfs://`, `collection://` | Use one of the allowed prefixes; treat custom scheme need as agent-side resolution |
| Random / fly endpoint TLS errors after idle | Fly auto-stop machine is cold; first request wakes it | warm-up: `curl /healthz` first, then real call |
| Mac unreachable after a few hours of `--http` mode | Mac slept | wrap the binary with `caffeinate -i`, or use launchd (§6) |

---

## 11 · See also

- [`docs/mcp.md`](./mcp.md) — MCP tool surface reference (one-liners per tool).
- [`docs/api.md`](./api.md) — HTTP API spec (`/mcp`, `/blob`, `/items/<id>/blob`).
- [`docs/architecture.md`](./architecture.md) — invariants, data model, why decisions were made.
- [`docs/sop.md`](./sop.md) — how Box dogfoods its own development via 程辙.
- The `box_manual` MCP tool returns a ~10 KB in-tool digest for cold-start agents.
