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
5. Physical content is addressed through `storage_uri`, not owned by Box.
6. Read paths must have a degraded fallback when Box is unavailable.
7. Semantic search is optional and external to Box.

## License

MIT
