-- Rate Bucket Lua Script (Token Bucket Algorithm)
-- This script implements the token bucket algorithm for rate limiting.
--
-- Keys:
--   KEYS[1] - Rate limit key (e.g., "rl:tenant:{abc123}:rpm")
--
-- Args:
--   ARGV[1] - Cost (units to consume)
--   ARGV[2] - Refill rate (units per second)
--   ARGV[3] - Capacity (max units)
--   ARGV[4] - Current timestamp (nanoseconds)
--
-- Returns:
--   {allowed (0/1), remaining (float)}

local key = KEYS[1]
local cost = tonumber(ARGV[1])
local refill_rate = tonumber(ARGV[2])
local capacity = tonumber(ARGV[3])
local now = tonumber(ARGV[4])

-- Get current state
local bucket = redis.call('HMGET', key, 'available', 'last_time')
local available = tonumber(bucket[1])
local last_time = tonumber(bucket[2])

-- Initialize if not exists
if available == nil then
    available = capacity
    last_time = now
end

-- Calculate refill
local elapsed = (now - last_time) / 1e9  -- Convert nanoseconds to seconds
available = available + elapsed * refill_rate

-- Cap at capacity
if available > capacity then
    available = capacity
end

-- Check if allowed
local allowed = 0
local remaining = available

if available >= cost then
    available = available - cost
    allowed = 1
    remaining = available
end

-- Update state
redis.call('HMSET', key, 'available', available, 'last_time', now)
redis.call('EXPIRE', key, 3600)  -- 1 hour TTL

return {allowed, remaining}
