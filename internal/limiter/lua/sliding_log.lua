local time = redis.call('TIME')
local now_ms = tonumber(time[1]) * 1000 + math.floor(tonumber(time[2]) / 1000)

local counts = {}
local denied_index = 0
local retry_ms = 0

for i = 1, #KEYS do
  local limit = tonumber(ARGV[2 * i - 1])
  local window_ms = tonumber(ARGV[2 * i])

  redis.call('ZREMRANGEBYSCORE', KEYS[i], '-inf', now_ms - window_ms)
  local count = redis.call('ZCARD', KEYS[i])
  counts[i] = count

  if count >= limit then
    if denied_index == 0 then
      denied_index = i
    end
    local oldest = redis.call('ZRANGE', KEYS[i], 0, 0, 'WITHSCORES')
    retry_ms = math.max(retry_ms, tonumber(oldest[2]) + window_ms - now_ms)
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

  local member = time[1] .. '.' .. time[2] .. '-' .. counts[i]
  redis.call('ZADD', KEYS[i], now_ms, member)
  redis.call('PEXPIRE', KEYS[i], window_ms)

  local remaining = limit - counts[i] - 1
  if min_remaining < 0 or remaining < min_remaining then
    min_remaining = remaining
    tightest = i
  end
end

return {1, min_remaining, 0, now_ms, tightest}
