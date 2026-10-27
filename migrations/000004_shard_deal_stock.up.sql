-- Split each deal's stock counters over several rows so concurrent
-- reservations of one deal stop queuing on a single row lock (ADR 0011).
-- Each shard owns a slice of the deal's units; its CHECK keeps that slice
-- from being oversold, so the deal as a whole cannot be oversold either.
CREATE TABLE deal_stock_shards (
  deal_id  BIGINT   NOT NULL REFERENCES deals(id),
  shard    SMALLINT NOT NULL CHECK (shard >= 0),
  capacity INT      NOT NULL CHECK (capacity > 0),
  reserved INT      NOT NULL DEFAULT 0 CHECK (reserved >= 0),
  sold     INT      NOT NULL DEFAULT 0 CHECK (sold >= 0),
  PRIMARY KEY (deal_id, shard),
  CONSTRAINT deal_stock_shards_not_exceeded CHECK (reserved + sold <= capacity)
);

-- Existing deals keep their counters in a single shard.
INSERT INTO deal_stock_shards (deal_id, shard, capacity, reserved, sold)
SELECT id, 0, total_stock, reserved, sold FROM deals;

-- The shard a reservation took its units from, so confirmation, cancellation
-- and expiry move them on that same row. Existing reservations map to shard 0.
ALTER TABLE reservations ADD COLUMN stock_shard SMALLINT NOT NULL DEFAULT 0;

ALTER TABLE deals DROP CONSTRAINT deals_stock_not_exceeded;
ALTER TABLE deals DROP COLUMN reserved;
ALTER TABLE deals DROP COLUMN sold;
