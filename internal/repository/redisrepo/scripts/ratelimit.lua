-- Token bucket for one caller. KEYS[1] holds tokens and last refill time.
-- ARGV: rate (tokens/sec), capacity, now_ms, cost.
-- Returns {allowed (0|1), remaining_tokens, retry_after_ms}.
--
-- Hash-tagged key so the bucket stays on one Redis Cluster slot with any
-- other per-user keys that share the same tag.

local key = KEYS[1]
local rate = tonumber(ARGV[1])
local capacity = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local cost = tonumber(ARGV[4])

if rate == nil or rate <= 0 or capacity == nil or capacity <= 0 or now == nil or cost == nil or cost <= 0 then
  return redis.error_reply("invalid ratelimit args")
end

local data = redis.call("HMGET", key, "tokens", "ts")
local tokens = tonumber(data[1])
local ts = tonumber(data[2])
if tokens == nil or ts == nil then
  tokens = capacity
  ts = now
end

local elapsed = (now - ts) / 1000.0
if elapsed < 0 then
  elapsed = 0
end
tokens = math.min(capacity, tokens + elapsed * rate)

local allowed = 0
local retry_ms = 0
if tokens >= cost then
  tokens = tokens - cost
  allowed = 1
else
  retry_ms = math.ceil((cost - tokens) / rate * 1000)
  if retry_ms < 1 then
    retry_ms = 1
  end
end

redis.call("HSET", key, "tokens", tokens, "ts", now)
-- Keep the key a little longer than a full refill so an idle caller
-- does not lose burst capacity between requests.
local ttl_ms = math.ceil((capacity / rate) * 2000)
if ttl_ms < 1000 then
  ttl_ms = 1000
end
redis.call("PEXPIRE", key, ttl_ms)

return {allowed, math.floor(tokens), retry_ms}
