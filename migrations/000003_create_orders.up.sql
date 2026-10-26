CREATE TABLE orders (
  id             UUID        PRIMARY KEY,
  reservation_id UUID        NOT NULL UNIQUE REFERENCES reservations(id),
  deal_id        BIGINT      NOT NULL REFERENCES deals(id),
  user_id        TEXT        NOT NULL,
  quantity       INT         NOT NULL,
  total_cents    BIGINT      NOT NULL,
  currency       CHAR(3)     NOT NULL,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
