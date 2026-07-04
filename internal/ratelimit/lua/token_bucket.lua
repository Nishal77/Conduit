-- Token bucket rate limiter
-- Keys: KEYS[1] = "rl:{tenant_id}:{scope}:{target}"
-- Args: ARGV[1] = capacity (max tokens)
--       ARGV[2] = refill_rate (tokens per second, float)
--       ARGV[3] = requested (tokens to consume, almost always 1)
--       ARGV[4] = now_us (current unix time in microseconds)
--       ARGV[5] = ttl_sec (key TTL in seconds, set to window*2)
--
-- Returns: {allowed, remaining}
--   allowed: 1 if the request is allowed, 0 if rate limited
--   remaining: integer tokens remaining after this request

local key = KEYS[1]
local capacity    = tonumber(ARGV[1])
local refill_rate = tonumber(ARGV[2])  -- tokens per second
local requested   = tonumber(ARGV[3])
local now_us      = tonumber(ARGV[4])
local ttl_sec     = tonumber(ARGV[5])

-- Read current state
local data = redis.call('HMGET', key, 'tokens', 'last_refill')
local tokens     = tonumber(data[1]) or capacity
local last_refill = tonumber(data[2]) or now_us

-- Refill tokens based on elapsed time
local elapsed_sec = math.max(0, (now_us - last_refill) / 1e6)
tokens = math.min(capacity, tokens + elapsed_sec * refill_rate)

-- Attempt to consume
local allowed = 0
if tokens >= requested then
    tokens = tokens - requested
    allowed = 1
end

-- Persist new state
redis.call('HMSET', key, 'tokens', tostring(tokens), 'last_refill', tostring(now_us))
redis.call('EXPIRE', key, ttl_sec)

return {allowed, math.floor(tokens)}
