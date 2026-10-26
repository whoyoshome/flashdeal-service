-- reserve.lua takes units for a buyer atomically: Redis runs the whole
-- script without interleaving other commands, so concurrent buyers on any
-- number of API replicas can never take more units than exist.
--
-- KEYS[1] = deal:{<id>}:stock   units left
-- KEYS[2] = deal:{<id>}:buyers  hash user_id -> units held
-- ARGV[1] = user_id   ARGV[2] = qty   ARGV[3] = per_user_limit
--
-- Returns the units left after reserving, or:
--   -1 sold out, -2 deal not loaded (not activated), -3 per-user limit
local stock = redis.call('GET', KEYS[1])
if not stock then
  return -2
end
stock = tonumber(stock)
local qty = tonumber(ARGV[2])
local bought = tonumber(redis.call('HGET', KEYS[2], ARGV[1]) or '0')
if bought + qty > tonumber(ARGV[3]) then
  return -3
end
if stock < qty then
  return -1
end
redis.call('DECRBY', KEYS[1], qty)
redis.call('HINCRBY', KEYS[2], ARGV[1], qty)
-- HINCRBY may have just created the buyers hash; give it the stock key's
-- expiry so it never outlives the deal.
local ttl = redis.call('PTTL', KEYS[1])
if ttl > 0 then
  redis.call('PEXPIRE', KEYS[2], ttl)
end
return stock - qty
