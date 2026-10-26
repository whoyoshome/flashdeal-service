-- release.lua gives back units taken by reserve.lua: after a failed
-- reservation (compensation), a cancellation or an expiry.
--
-- KEYS[1] = deal:{<id>}:stock   KEYS[2] = deal:{<id>}:buyers
-- ARGV[1] = user_id   ARGV[2] = qty
--
-- Returns the units left after releasing, or -2 when the deal is not
-- loaded (expired or never activated): there is nothing to give back to.
if redis.call('EXISTS', KEYS[1]) == 0 then
  return -2
end
local qty = tonumber(ARGV[2])
local left = redis.call('INCRBY', KEYS[1], qty)
-- Never drive a buyer below zero; entries are kept at 0 rather than deleted
-- so the hash, and its expiry, survive.
local bought = tonumber(redis.call('HGET', KEYS[2], ARGV[1]) or '0')
redis.call('HSET', KEYS[2], ARGV[1], math.max(bought - qty, 0))
local ttl = redis.call('PTTL', KEYS[1])
if ttl > 0 then
  redis.call('PEXPIRE', KEYS[2], ttl)
end
return left
