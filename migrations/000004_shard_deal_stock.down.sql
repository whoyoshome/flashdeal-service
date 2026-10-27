ALTER TABLE deals ADD COLUMN reserved INT NOT NULL DEFAULT 0 CHECK (reserved >= 0);
ALTER TABLE deals ADD COLUMN sold INT NOT NULL DEFAULT 0 CHECK (sold >= 0);

UPDATE deals AS d
SET reserved = s.reserved, sold = s.sold
FROM (
  SELECT deal_id, sum(reserved)::int AS reserved, sum(sold)::int AS sold
  FROM deal_stock_shards
  GROUP BY deal_id
) AS s
WHERE s.deal_id = d.id;

ALTER TABLE deals ADD CONSTRAINT deals_stock_not_exceeded CHECK (reserved + sold <= total_stock);

ALTER TABLE reservations DROP COLUMN stock_shard;

DROP TABLE deal_stock_shards;
