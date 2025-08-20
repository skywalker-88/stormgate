-- Redis Lua script implementing a distributed token bucket with variable cost.
-- KEYS[1] = bucket key
-- ARGV[1] = now_ms
-- ARGV[2] = refill_rate tokens per second (float allowed)
-- ARGV[3] = burst_capacity (max tokens)
-- ARGV[4] = cost (tokens to consume)
-- Returns: {allowed(0/1), tokens_remaining(float), retry_after_ms(int), reset_ms(int)}

local key       = KEYS[1]
local now_ms    = tonumber(ARGV[1])
local rate      = tonumber(ARGV[2])
local burst     = tonumber(ARGV[3])
local cost      = tonumber(ARGV[4])

local data = redis.call('HMGET', key, 'tokens', 'ts')
local tokens = tonumber(data[1])
local ts     = tonumber(data[2])

if tokens == nil then
  tokens = burst
  ts = now_ms
end

-- Refill based on elapsed time
local elapsed = (now_ms - ts) / 1000.0
if elapsed < 0 then elapsed = 0 end
local refill = elapsed * rate

tokens = math.min(burst, tokens + refill)
local allowed = 0
local retry_ms = 0

if tokens >= cost then
  tokens = tokens - cost
  allowed = 1
  ts = now_ms
else
  allowed = 0
  local deficit = cost - tokens
  retry_ms = math.floor((deficit / rate) * 1000 + 0.5)
  -- move time forward so next caller sees the same refill baseline
  ts = now_ms
end

-- Persist state and set a TTL ~ 2x full-refill time (never 0)
redis.call('HMSET', key, 'tokens', tokens, 'ts', ts)
local ttl = math.floor((burst / math.max(rate, 0.0001)) * 2 + 0.5)
if ttl < 1 then ttl = 1 end
redis.call('EXPIRE', key, ttl)

-- time to full reset (when bucket would be full again)
local reset_ms = math.floor(((burst - tokens) / math.max(rate, 0.0001)) * 1000 + 0.5)

return {allowed, tokens, retry_ms, reset_ms}