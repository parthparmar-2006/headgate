local key = KEYS[1]
local limit = tonumber(ARGV[1])
local window_ms = tonumber(ARGV[2])

local time = redis.call('TIME')
local now_ms = tonumber(time[1]) * 1000 + math.floor(tonumber(time[2]) / 1000)
local window_start = now_ms - (now_ms % window_ms)
local window_end = window_start + window_ms

local count = 0
local stored_start = tonumber(redis.call('HGET', key, 'start'))
if stored_start == window_start then
  count = tonumber(redis.call('HGET', key, 'count'))
end

if count >= limit then
  return {0, 0, window_end - now_ms, now_ms}
end

count = count + 1
redis.call('HSET', key, 'start', window_start, 'count', count)
redis.call('PEXPIRE', key, window_ms * 2)
return {1, limit - count, 0, now_ms}
