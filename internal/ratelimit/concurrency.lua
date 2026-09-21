-- Concurrency Semaphore Lua Script (atomic acquire)
-- Atomically increments the concurrency counter, checks it against the max,
-- and rolls back if it exceeds the limit. Replaces the non-atomic
-- INCR -> check -> DECR sequence that allowed concurrent over-admission.
--
-- Keys:
--   KEYS[1] - Concurrency key (e.g., "rl:tenant:{abc123}:concurrency")
--
-- Args:
--   ARGV[1] - Cost (units to acquire, normally 1)
--   ARGV[2] - Max concurrency
--   ARGV[3] - TTL seconds (safety net for orphaned keys)
--
-- Returns:
--   {allowed (0/1), current (int)}

local key = KEYS[1]
local cost = tonumber(ARGV[1])
local max = tonumber(ARGV[2])
local ttl = tonumber(ARGV[3])

local current = redis.call('INCRBY', key, cost)
redis.call('EXPIRE', key, ttl)

if current > max then
    redis.call('DECRBY', key, cost)
    return {0, current - cost}
end

return {1, current}
