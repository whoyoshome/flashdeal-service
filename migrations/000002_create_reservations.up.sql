CREATE TABLE reservations (
  id              UUID        PRIMARY KEY,
  deal_id         BIGINT      NOT NULL REFERENCES deals(id),
  user_id         TEXT        NOT NULL,
  quantity        INT         NOT NULL CHECK (quantity > 0),
  status          TEXT        NOT NULL CHECK (status IN ('pending', 'confirmed', 'expired', 'cancelled')),
  idempotency_key TEXT        NOT NULL,
  expires_at      TIMESTAMPTZ NOT NULL,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT reservations_idempotent UNIQUE (user_id, idempotency_key)
);

CREATE INDEX reservations_pending_expiry_idx
  ON reservations (expires_at) WHERE status = 'pending';
CREATE INDEX reservations_deal_user_idx ON reservations (deal_id, user_id);
