-- Quota correction: adjust pre-deducted usage by a delta (positive or
-- negative), flooring at zero. INCRBY preserves any existing TTL so the key
-- still expires at the window boundary.
--
-- Keys:
--   KEYS[1] - Quota key (e.g., "rl:tenant:{abc123}:quota:daily")
--
-- Args:
--   ARGV[1] - Delta (actual - pre_deducted; may be negative)
--
-- Returns:
--   {used (int)}

local key = KEYS[1]
local delta = tonumber(ARGV[1])

local current = redis.call('INCRBY', key, delta)
if current <= 0 then
    redis.call('DEL', key)
    return {0}
end

return {current}
