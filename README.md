# Box Model

Box is a small protocol for storing and browsing AI workflow outputs as a
multi-dimensional index.

It is not a database replacement. It sits above source tables, files, object
storage, repositories, and semantic indexes, giving agents and applications one
stable way to answer:

- what artifact exists
- where it came from
- how it is labeled
- where the physical content lives
- who consumed it later

## Core Idea

```
producer -> Box index -> storage_uri -> physical storage
                    ^
                    |
             optional Astrolabe
          intent/query -> locations
```

Box handles exact, governed browsing by labels, source references, kinds, and
locations. An optional semantic layer such as Astrolabe can translate intent
into `location_id` values before Box browsing.

## Concepts

| Concept | Meaning |
| --- | --- |
| Box | A named container/index owned by an area, room, user, or standalone app |
| Item | One indexed output, pointer, decision, document, row, blob, or external asset |
| SourceRef | Provenance JSON such as `artifact_id`, `task_id`, `node_id`, `revision` |
| Labels | Namespaced metadata for browsing and governance |
| StorageURI | Pointer to the real content, such as `row://`, `blob://`, `s3://`, `repo://` |
| ConsumeLog | Audit trail of downstream readers |

## Quick Start

```bash
go test ./...
go run ./cmd/box-demo
```

Expected demo output:

```json
{
  "items": [
    {
      "kind": "document",
      "storage_uri": "blob://sha256:..."
    }
  ]
}
```

## Repository Layout

```text
box/                Go reference implementation
cmd/box-demo/       Minimal runnable demo
docs/architecture.md
docs/api.md
docs/schema.sql
docs/operations.md
examples/
```

## Design Invariants

1. Source systems remain the source of truth.
2. Box rows are rebuildable from source tables and outbox history.
3. Writes are idempotent by `idem_key`.
4. Labels are namespaced; reserved namespaces start with `__`.
5. Items may carry inline `content` as a self-contained copy; `storage_uri` remains required as the upstream provenance pointer (the SoR for regeneration). When `content` is present, Box owns a *copy* of it, but regeneration-authoritative reads still go through `storage_uri`.
6. Read paths must have a degraded fallback when Box is unavailable.
7. Semantic search is optional and external to Box.
10. **Box is dumb storage. Intelligence belongs to the agent.** Box validates schemas (e.g. PassCriteria.Kind enum, Goal length, Symbol validity) but NEVER interprets them. Box does not run `pass_criteria.query` to decide if a task is complete — the agent must do that and set the task status explicitly via `SetItemSymbols`. Box does not enforce `nail_chain` order or invoke nails — the agent loads nail YAML and calls LLMs itself.
11. **Tokens (程符) are session state, not authorization. Writes without a token still succeed (storage-only contract). Process restart invalidates all tokens.** Program-track sessions (`StartYiCheng` / `FinishYiCheng` / `AbortYiCheng`) live only in process memory (`sync.Map`). A token identifies one execution path so opt-in writes can auto-attach trace events; it does NOT gate access. Box never persists tokens — by design — so a restart wipes every live session while leaving the on-disk task trace intact.
12. **Box is "符径(Symbol Path)" — not a database.** See `docs/semantic_redesign.md` for the lexicon SoR. The program-track layer uses path-ledger semantics: every event (start / write / finish / abort) appends to a per-task `trace.jsonl`, and the visible "status" (✓ / ✗ / ? / → / ~ / ◯) is a cursor over that ledger that any later event may flip. There is no terminal state; `FinishYiCheng` is just one more event that paints a ✓ cursor.

## License

MIT
