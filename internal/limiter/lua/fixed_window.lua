local time = redis.call('TIME')
local now_ms = tonumber(time[1]) * 1000 + math.floor(tonumber(time[2]) / 1000)

local counts = {}
local starts = {}
local denied_index = 0
local retry_ms = 0

for i = 1, #KEYS do
  local limit = tonumber(ARGV[2 * i - 1])
  local window_ms = tonumber(ARGV[2 * i])
  local window_start = now_ms - (now_ms % window_ms)

  local count = 0
  local stored_start = tonumber(redis.call('HGET', KEYS[i], 'start'))
  if stored_start == window_start then
    count = tonumber(redis.call('HGET', KEYS[i], 'count'))
  end
  counts[i] = count
  starts[i] = window_start

  if count >= limit then
    if denied_index == 0 then
      denied_index = i
    end
    retry_ms = math.max(retry_ms, window_start + window_ms - now_ms)
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
  local count = counts[i] + 1

  redis.call('HSET', KEYS[i], 'start', starts[i], 'count', count)
  redis.call('PEXPIRE', KEYS[i], window_ms * 2)

  local remaining = limit - count
  if min_remaining < 0 or remaining < min_remaining then
    min_remaining = remaining
    tightest = i
  end
end

return {1, min_remaining, 0, now_ms, tightest}
