-- Concurrency Semaphore Release Lua Script (floor at 0)
-- Atomically decrements the concurrency counter without going negative.
-- A bare DECR could be driven below zero by a double release or a release
-- without a matching acquire, corrupting subsequent limit checks.
--
-- Keys:
--   KEYS[1] - Concurrency key (e.g., "rl:tenant:{abc123}:concurrency")
--
-- Args:
--   ARGV[1] - Amount to release (normally 1)
--
-- Returns:
--   {current (int)}

local key = KEYS[1]
local amount = tonumber(ARGV[1])

local current = tonumber(redis.call('GET', key)) or 0

if current <= amount then
    redis.call('DEL', key)
    return {0}
end

redis.call('DECRBY', key, amount)
return {current - amount}
