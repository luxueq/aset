-- Fixed-window quota counter (daily/monthly token budget).
-- Atomically increments the counter and checks it against the limit, rolling
-- back if it would exceed. The TTL is set to the remaining window time on
-- first touch only, so the key expires at the calendar boundary regardless of
-- subsequent touches.
--
-- Keys:
--   KEYS[1] - Quota key (e.g., "rl:tenant:{abc123}:quota:daily")
--
-- Args:
--   ARGV[1] - Cost (tokens to consume)
--   ARGV[2] - Limit (max tokens in window; 0 = unlimited)
--   ARGV[3] - TTL seconds (seconds until window end)
--
-- Returns:
--   {allowed (0/1), used (int), limit (int)}

local key = KEYS[1]
local cost = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
local ttl = tonumber(ARGV[3])

if limit <= 0 then
    return {1, 0, limit}
end

local exists = redis.call('EXISTS', key)
local used = redis.call('INCRBY', key, cost)
if exists == 0 then
    redis.call('EXPIRE', key, ttl)
end

if used > limit then
    redis.call('DECRBY', key, cost)
    return {0, limit, limit}
end

return {1, used, limit}
