-- Sliding Window Lua Script
-- This script implements the sliding window algorithm for rate limiting.
-- More accurate than fixed window, prevents burst at window boundaries.
--
-- Keys:
--   KEYS[1] - Rate limit key (e.g., "rl:tenant:{abc123}:rpm")
--
-- Args:
--   ARGV[1] - Limit (max units per window)
--   ARGV[2] - Window size (milliseconds)
--   ARGV[3] - Current timestamp (milliseconds)
--   ARGV[4] - Cost (units to consume, default 1)
--
-- Returns:
--   {allowed (0/1), remaining (int), retry_after_ms (int)}
--
-- Cost semantics: a check with cost N consumes N window slots. This matters
-- for lease refills in hybrid mode (one batch call consumes a batch of
-- quota). Cost-weighted entries are stored as N ZSET members; a zero-cost
-- probe consumes nothing and is a pure read.

local key = KEYS[1]
local limit = tonumber(ARGV[1])
local window_size = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local cost = tonumber(ARGV[4]) or 1

-- Calculate window start
local window_start = now - window_size

-- Remove expired entries outside the window
redis.call('ZREMRANGEBYSCORE', key, '-inf', window_start)

-- Count current entries in window
local count = redis.call('ZCARD', key)

-- Check if allowed
local allowed = 0
local remaining = 0
local retry_after_ms = 0

if count + cost <= limit then
    -- Allowed: add cost entries (weighted sliding window: a batch check of
    -- cost N consumes N window slots, not one)
    for i = 1, cost do
        -- timestamp + random + index ensures uniqueness across concurrent
        -- batch checks within the same millisecond
        local member = tostring(now) .. ':' .. tostring(math.random(1000000)) .. ':' .. tostring(i)
        redis.call('ZADD', key, now, member)
    end
    redis.call('PEXPIRE', key, window_size + 1000)  -- Add 1s buffer

    allowed = 1
    remaining = limit - count - cost
else
    -- Denied: calculate retry_after
    -- Find the oldest entry to determine when it will expire
    local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
    if #oldest > 0 then
        local oldest_time = tonumber(oldest[2])
        retry_after_ms = (oldest_time + window_size) - now
        if retry_after_ms < 0 then
            retry_after_ms = 0
        end
    else
        -- Fallback: retry after full window
        retry_after_ms = window_size
    end

    -- Remaining is what could still be admitted (limit - current count),
    -- NOT zero: hybrid lease refills use it to request a smaller lease.
    remaining = limit - count
    if remaining < 0 then
        remaining = 0
    end
end

return {allowed, remaining, retry_after_ms}
