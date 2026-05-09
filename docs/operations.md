# Operations

## Recommended Checks

- `box_items` write error rate
- browse p50/p95/p99 by query template
- idempotency conflict rate
- label payload size rejection count
- resolver failures by storage URI scheme
- consume log write failures

## SLO Starting Points

| Operation | Initial target |
| --- | --- |
| label browse p95 | <= 200ms at 1M rows |
| location browse p95 | <= 300ms at 1M rows |
| store item p99 | <= 1s without semantic registration |
| rebuild lag | <= 5 minutes |

## Backup and Rebuild

Box is derived. Back up the source systems and the event stream first. Then back
up Box for fast recovery.

Rebuild strategy:

1. Truncate or create a new Box index.
2. Replay source outbox events in source order.
3. Preserve idempotency keys.
4. Re-run semantic registration when location data is missing or stale.
5. Compare counts by source kind and source id.

## Public Deployment Advice

- Put API auth in front of write endpoints.
- Keep browse endpoints read-only and bounded by `limit`.
- Cap label values and source reference payloads.
- Treat unknown storage URI schemes as validation errors.
- Keep semantic indexing outside the synchronous hot path.
