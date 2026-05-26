CREATE TABLE boxes (
  box_id          TEXT PRIMARY KEY,
  box_key         TEXT NOT NULL,
  version         INTEGER NOT NULL DEFAULT 1,
  owner_type      TEXT NOT NULL CHECK (owner_type IN ('room', 'area', 'user', 'standalone')),
  owner_id        TEXT NOT NULL,
  storage_policy  JSONB NOT NULL DEFAULT '{"allowed_formats":["json","markdown","text"],"max_items":1000}',
  labels          JSONB NOT NULL DEFAULT '{}',
  status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'sealed', 'archived')),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (box_key, version)
);

CREATE TABLE box_items (
  item_id       TEXT PRIMARY KEY,
  box_id        TEXT NOT NULL REFERENCES boxes(box_id),
  idem_key      TEXT NOT NULL,
  kind          TEXT NOT NULL,
  source_type   TEXT NOT NULL,
  source_ref    JSONB NOT NULL DEFAULT '{}',
  labels        JSONB NOT NULL DEFAULT '{}',
  location_id   TEXT,
  storage_uri   TEXT NOT NULL,
  format        TEXT NOT NULL,
  content       JSONB,
  content_hash  TEXT NOT NULL,
  metadata      JSONB NOT NULL DEFAULT '{}',
  stored_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  status        TEXT NOT NULL DEFAULT 'available' CHECK (status IN ('available', 'consumed', 'expired', 'deleted')),
  stored_by     TEXT,
  UNIQUE (box_id, idem_key)
);

CREATE INDEX idx_box_items_box_status ON box_items (box_id, status);
CREATE INDEX idx_box_items_location ON box_items (location_id, stored_at DESC) WHERE location_id IS NOT NULL;
CREATE INDEX idx_box_items_kind_ts ON box_items (kind, stored_at DESC);
CREATE INDEX idx_box_items_labels ON box_items USING GIN (labels jsonb_path_ops);
CREATE INDEX idx_box_items_source_ref ON box_items USING GIN (source_ref jsonb_path_ops);

CREATE TABLE box_consumes (
  consume_id    TEXT PRIMARY KEY,
  item_id       TEXT NOT NULL REFERENCES box_items(item_id),
  consumer_type TEXT NOT NULL CHECK (consumer_type IN ('room', 'agent', 'user', 'external')),
  consumer_id   TEXT NOT NULL,
  purpose       TEXT,
  consumed_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_box_consumes_item ON box_consumes (item_id);

-- R0.2: Item revision chain. Stored as ALTER TABLE deltas to make the
-- migration story explicit; the canonical schema is the base table above
-- plus these additive changes.
ALTER TABLE box_items ADD COLUMN revision_of    TEXT REFERENCES box_items(item_id);
ALTER TABLE box_items ADD COLUMN revision       INTEGER NOT NULL DEFAULT 1;
ALTER TABLE box_items ADD COLUMN is_latest      BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE box_items ADD COLUMN superseded_at  TIMESTAMPTZ;

-- Default Browse path only sees IsLatest rows; this partial index keeps the
-- hot path cheap regardless of how deep the version chain grows.
CREATE INDEX idx_box_items_latest ON box_items (box_id, stored_at DESC) WHERE is_latest;
