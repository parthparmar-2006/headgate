local key = KEYS[1]
local limit = tonumber(ARGV[1])
local window_ms = tonumber(ARGV[2])

local time = redis.call('TIME')
local now_ms = tonumber(time[1]) * 1000 + math.floor(tonumber(time[2]) / 1000)
local window_start = now_ms - (now_ms % window_ms)
local elapsed = now_ms - window_start

local current = 0
local previous = 0
local stored = redis.call('HMGET', key, 'start', 'current', 'previous')
local stored_start = tonumber(stored[1])
if stored_start == window_start then
  current = tonumber(stored[2])
  previous = tonumber(stored[3])
elseif stored_start == window_start - window_ms then
  previous = tonumber(stored[2])
end

local previous_weight = (window_ms - elapsed) / window_ms
local estimated = previous * previous_weight + current

if estimated + 1 > limit then
  local retry_ms = window_ms - elapsed
  if current + 1 <= limit then
    local previous_budget = limit - current - 1
    local elapsed_needed = window_ms - (previous_budget * window_ms / previous)
    retry_ms = math.ceil(elapsed_needed - elapsed)
  end
  return {0, 0, math.max(retry_ms, 1), now_ms}
end

current = current + 1
redis.call('HSET', key, 'start', window_start, 'current', current, 'previous', previous)
redis.call('PEXPIRE', key, window_ms * 2)
return {1, math.floor(limit - estimated - 1), 0, now_ms}
