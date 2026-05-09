# Architecture

Box is a governed index over artifacts and assets. It keeps enough metadata to
browse and audit content, while leaving physical storage to existing systems.

## Two-Layer Search

Box supports exact browsing:

```text
labels + source_ref + kind + location_id -> storage_uri
```

For semantic browsing, pair it with a coordinate provider:

```text
query -> Astrolabe locate -> location_id[] -> Box browse -> storage_uri[]
```

Astrolabe is intentionally optional. Box can run alone for deterministic,
metadata-driven access.

## Write Flow

1. Producer writes to the source of truth.
2. Producer or outbox worker registers a Box item with an idempotency key.
3. Box stores labels, source reference, location, storage URI, and content hash.
4. Consumers browse Box and fetch the underlying content through a resolver.

## Storage URI Schemes

| Scheme | Meaning |
| --- | --- |
| `row://table/id` | Existing OLTP row |
| `blob://sha256:...` | Content-addressed blob |
| `folder://...` | Filesystem or object-store prefix |
| `repo://host/org/repo@sha:/path` | Git repository snapshot |
| `s3://bucket/key` | Object storage |
| `ipfs://cid/path` | IPFS content |
| `collection://name/doc` | External document collection |

## Label Namespaces

Reserved namespaces start with `__`:

| Namespace | Purpose |
| --- | --- |
| `__op:*` | Operational metadata: area, room, task, caller |
| `__sem:*` | Semantic metadata: topic, language, cluster |
| `__pii:*` | Privacy tags |
| `__gate:*` | Quality or review gates |

Application labels may use their own namespaces, but public implementations
should cap label value size and document performance guarantees.

## Failure Model

Box should be treated as rebuildable infrastructure.

- Losing Box should not delete source content.
- A failed Box write can be replayed from an outbox.
- A failed read should degrade to known source-table fallbacks where possible.
- Semantic index drift should be repaired by reconciliation jobs.
