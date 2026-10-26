-- restore.lua replaces a deal's gate state with the one PostgreSQL implies,
-- as a compare-and-set: it applies only if the stock still holds the value
-- the reconciler read, so a reservation or release that slipped in between
-- is never overwritten. Redis runs the script atomically.
--
-- KEYS[1] = deal:{<id>}:stock   KEYS[2] = deal:{<id>}:buyers
-- ARGV[1] = expected stock   ARGV[2] = new stock
-- ARGV[3], ARGV[4], ... = user_id, units held (pairs)
--
-- Returns 1 when applied, 0 when the stock changed or is not loaded.
local current = redis.call('GET', KEYS[1])
if not current or tonumber(current) ~= tonumber(ARGV[1]) then
  return 0
end
-- KEEPTTL: the stock key's expiry follows the deal's end, not the repair.
redis.call('SET', KEYS[1], ARGV[2], 'KEEPTTL')
redis.call('DEL', KEYS[2])
-- One HSET per buyer rather than unpack(ARGV): unpack is bounded by the Lua
-- stack, which a deal with thousands of buyers would exceed.
for i = 3, #ARGV, 2 do
  redis.call('HSET', KEYS[2], ARGV[i], ARGV[i + 1])
end
local ttl = redis.call('PTTL', KEYS[1])
if ttl > 0 and #ARGV > 2 then
  redis.call('PEXPIRE', KEYS[2], ttl)
end
return 1
