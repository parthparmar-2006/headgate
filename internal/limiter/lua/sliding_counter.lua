local time = redis.call('TIME')
local now_ms = tonumber(time[1]) * 1000 + math.floor(tonumber(time[2]) / 1000)

local function retry_for(limit, window_ms, elapsed, current, previous)
  local retry_ms = window_ms - elapsed
  if current + 1 <= limit then
    local previous_budget = limit - current - 1
    local elapsed_needed = window_ms - (previous_budget * window_ms / previous)
    retry_ms = math.ceil(elapsed_needed - elapsed)
  end
  return math.max(retry_ms, 1)
end

local starts = {}
local currents = {}
local previouses = {}
local estimates = {}
local denied_index = 0
local retry_ms = 0

for i = 1, #KEYS do
  local limit = tonumber(ARGV[2 * i - 1])
  local window_ms = tonumber(ARGV[2 * i])
  local window_start = now_ms - (now_ms % window_ms)
  local elapsed = now_ms - window_start

  local current = 0
  local previous = 0
  local stored = redis.call('HMGET', KEYS[i], 'start', 'current', 'previous')
  local stored_start = tonumber(stored[1])
  if stored_start == window_start then
    current = tonumber(stored[2])
    previous = tonumber(stored[3])
  elseif stored_start == window_start - window_ms then
    previous = tonumber(stored[2])
  end

  local estimated = previous * ((window_ms - elapsed) / window_ms) + current
  starts[i] = window_start
  currents[i] = current
  previouses[i] = previous
  estimates[i] = estimated

  if estimated + 1 > limit then
    if denied_index == 0 then
      denied_index = i
    end
    retry_ms = math.max(retry_ms, retry_for(limit, window_ms, elapsed, current, previous))
  end
end

if denied_index > 0 then
  return {0, 0, retry_ms, now_ms, denied_index}
end

local min_remaining = -1
local tightest = 1
for i = 1, #KEYS do
  local limit = tonumber(ARGV[2 * i - 1])
  local window_ms = tonumber(ARGV[2 * i])

  redis.call('HSET', KEYS[i], 'start', starts[i], 'current', currents[i] + 1, 'previous', previouses[i])
  redis.call('PEXPIRE', KEYS[i], window_ms * 2)

  local remaining = math.floor(limit - estimates[i] - 1)
  if min_remaining < 0 or remaining < min_remaining then
    min_remaining = remaining
    tightest = i
  end
end

return {1, min_remaining, 0, now_ms, tightest}
