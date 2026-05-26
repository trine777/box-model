# API Shape

This repository ships a Go API, but the model maps cleanly to HTTP.

## Create Box

```http
POST /boxes
```

```json
{
  "key": "area-deliverables",
  "owner_type": "area",
  "owner_id": "area-123",
  "storage_policy": {
    "allowed_formats": ["json", "markdown"],
    "max_items": 1000
  }
}
```

## Store Item

```http
POST /boxes/{box_id}/items
```

```json
{
  "idem_key": "artifact:art-123:v1",
  "kind": "document",
  "source_type": "artifact",
  "source_ref": {
    "artifact_id": "art-123",
    "task_id": "task-1",
    "node_id": "draft",
    "revision": "1"
  },
  "labels": {
    "__op:area_id": "area-123",
    "__sem:topic": "billing"
  },
  "location_id": "loc-billing",
  "storage_uri": "row://artifacts/art-123?field=content",
  "format": "json"
}
```

## Browse

```http
GET /boxes/{box_id}/items?kind=document&label.__sem:topic=billing&ref.task_id=task-1
```

The response returns Box item metadata and storage URIs. Fetching physical
content is delegated to the URI resolver for each scheme.

## Get Item

```http
GET /items/{item_id}
```

Reading an item should append a consume log:

```json
{
  "consumer_type": "user",
  "consumer_id": "user-123",
  "purpose": "review"
}
```

## Replace Item

```http
POST /items/{item_id}/revisions
```

Opens a new revision of `{item_id}`. The previous item is flipped to
`status: "superseded"`, `is_latest: false`, and `superseded_at` is stamped.
The new item is returned with `revision = prev.revision + 1`, `is_latest: true`,
and `revision_of = prev.item_id`. `kind` is inherited from the prior revision
when omitted; supplying a different `kind` is rejected with a validation error.
`idem_key` defaults to `{prev.idem_key}/r{new_revision}` when not supplied.

```json
{
  "idem_key": "artifact:art-123:v2",
  "kind": "document",
  "source_type": "artifact",
  "source_ref": {
    "artifact_id": "art-123",
    "revision": "2"
  },
  "labels": {
    "__op:area_id": "area-123",
    "__sem:topic": "billing"
  },
  "storage_uri": "row://artifacts/art-123?field=content&rev=2",
  "format": "json"
}
```

By default Browse returns only `is_latest` items. Pass `include_history=true`
to see the full chain or `only_history=true` to see superseded revisions
only; the two flags are mutually exclusive.

## Patch Labels

```http
PATCH /items/{item_id}/labels
```

Replaces the labels on an item without opening a new revision. The item's
`revision`, `is_latest`, `content_hash`, and `storage_uri` are untouched.
The body is the full label set (not a merge); send `{}` to clear labels.

```json
{
  "labels": {
    "__sem:topic": "billing",
    "__op:priority": "high"
  }
}
```
