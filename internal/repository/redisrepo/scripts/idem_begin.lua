-- idem_begin.lua claims an idempotency key atomically: two retries arriving
-- at the same moment can never both own the key.
--
-- KEYS[1] = idem:{<user_id>}:<key>
-- ARGV[1] = in-progress record (JSON)   ARGV[2] = claim TTL in milliseconds
--
-- Returns the existing record when the key is taken, or false when this
-- call claimed it.
local current = redis.call('GET', KEYS[1])
if current then
  return current
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
return false
