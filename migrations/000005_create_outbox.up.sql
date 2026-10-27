-- Transactional outbox (ADR 0012): an event is inserted in the same
-- statement as the change it describes and deleted once a relay has
-- published it, so the table only holds events not yet published.
CREATE TABLE outbox (
  id           BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  event_type   TEXT        NOT NULL,
  aggregate_id TEXT        NOT NULL,
  payload      JSONB       NOT NULL,
  occurred_at  TIMESTAMPTZ NOT NULL
);
