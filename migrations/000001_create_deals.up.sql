CREATE TABLE deals (
  id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  product_id     TEXT        NOT NULL,
  title          TEXT        NOT NULL,
  price_cents    BIGINT      NOT NULL CHECK (price_cents > 0),
  currency       CHAR(3)     NOT NULL,
  total_stock    INT         NOT NULL CHECK (total_stock > 0),
  reserved       INT         NOT NULL DEFAULT 0 CHECK (reserved >= 0),
  sold           INT         NOT NULL DEFAULT 0 CHECK (sold >= 0),
  per_user_limit INT         NOT NULL DEFAULT 1 CHECK (per_user_limit > 0),
  starts_at      TIMESTAMPTZ NOT NULL,
  ends_at        TIMESTAMPTZ NOT NULL,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT deals_window_valid CHECK (ends_at > starts_at),
  CONSTRAINT deals_stock_not_exceeded CHECK (reserved + sold <= total_stock)
);
