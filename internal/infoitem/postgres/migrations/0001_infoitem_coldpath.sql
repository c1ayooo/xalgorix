CREATE TABLE IF NOT EXISTS info_items (
    id                TEXT        NOT NULL,
    scan_id           TEXT        NOT NULL,
    source_seq        BIGINT      NOT NULL CHECK (source_seq > 0),
    nature            TEXT        NOT NULL CHECK (nature IN ('observed', 'inferred')),
    content           JSONB       NOT NULL,
    coordinates       JSONB       NOT NULL,
    source            JSONB       NOT NULL,
    subject           JSONB       NOT NULL,
    purpose           TEXT        NOT NULL DEFAULT '',
    context           JSONB       NOT NULL DEFAULT '{}'::jsonb,
    relation_ids      JSONB       NOT NULL DEFAULT '[]'::jsonb,
    supports          JSONB       NOT NULL DEFAULT '[]'::jsonb,
    confidence        DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (confidence >= 0 AND confidence <= 1),
    coordinate_status TEXT        NOT NULL DEFAULT 'complete'
                                 CHECK (coordinate_status IN ('complete', 'partial', 'pending')),
    valid_from        TIMESTAMPTZ NULL,
    valid_until       TIMESTAMPTZ NULL,
    observed_at       TIMESTAMPTZ NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (scan_id, id),
    CHECK (valid_until IS NULL OR valid_from IS NULL OR valid_until > valid_from)
);

CREATE INDEX IF NOT EXISTS info_items_scan_nature_created_idx
    ON info_items (scan_id, nature, created_at, id);

CREATE INDEX IF NOT EXISTS info_items_subject_idx
    ON info_items (scan_id, ((subject ->> 'identifier')), created_at DESC);

CREATE INDEX IF NOT EXISTS info_items_purpose_idx
    ON info_items (scan_id, purpose, created_at DESC);

CREATE INDEX IF NOT EXISTS info_items_context_gin_idx
    ON info_items USING GIN (context);

CREATE TABLE IF NOT EXISTS info_item_edges (
    scan_id     TEXT        NOT NULL,
    source_seq  BIGINT      NOT NULL CHECK (source_seq > 0),
    from_id     TEXT        NOT NULL,
    to_id       TEXT        NOT NULL,
    type        TEXT        NOT NULL CHECK (type IN (
                                  'supports', 'contradicts', 'references',
                                  'same_session', 'same_subject')),
    weight      DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (weight >= 0 AND weight <= 1),
    created_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (scan_id, source_seq, from_id, to_id, type),
    CHECK (from_id <> to_id)
);

CREATE INDEX IF NOT EXISTS info_item_edges_from_idx
    ON info_item_edges (scan_id, from_id, type);

CREATE INDEX IF NOT EXISTS info_item_edges_to_idx
    ON info_item_edges (scan_id, to_id, type);

CREATE TABLE IF NOT EXISTS info_audit_records (
    scan_id          TEXT        NOT NULL,
    seq              BIGINT      NOT NULL CHECK (seq > 0),
    at               TIMESTAMPTZ NOT NULL,
    actor            JSONB       NOT NULL,
    operation        TEXT        NOT NULL,
    hypothesis_id    TEXT        NOT NULL DEFAULT '',
    evidence_id      TEXT        NOT NULL DEFAULT '',
    summary          TEXT        NOT NULL DEFAULT '',
    before_state     JSONB       NULL,
    after_state      JSONB       NULL,
    payload          JSONB       NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (scan_id, seq)
);

CREATE INDEX IF NOT EXISTS info_audit_records_hypothesis_idx
    ON info_audit_records (scan_id, hypothesis_id, seq);

CREATE TABLE IF NOT EXISTS info_sync_watermarks (
    scan_id      TEXT        PRIMARY KEY,
    applied_seq  BIGINT      NOT NULL DEFAULT 0 CHECK (applied_seq >= 0),
    status       TEXT        NOT NULL DEFAULT 'pending'
                             CHECK (status IN ('pending', 'applied', 'gap', 'failed')),
    last_error   TEXT        NOT NULL DEFAULT '',
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
