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
