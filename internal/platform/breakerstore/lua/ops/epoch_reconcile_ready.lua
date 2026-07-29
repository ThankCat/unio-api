local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end
local marker = KEYS[1]
local revision = tonumber(ARGV[2])
if ARGV[1] == '' or revision == nil or revision < 1 or ARGV[3] == '' then
  return redis.error_reply('invalid ready epoch')
end
local typ = redis.call('TYPE', marker)
if type(typ) == 'table' then typ = typ['ok'] end
if
  typ == 'hash'
  and redis.call('HGET', marker, 'state') == 'ready'
  and redis.call('HGET', marker, 'epoch') == ARGV[1]
  and redis.call('HGET', marker, 'revision') == ARGV[2]
  and redis.call('HGET', marker, 'marker_hash') == ARGV[3]
then
  return { 'unchanged' }
end
redis.call('DEL', marker)
redis.call(
  'HSET',
  marker,
  'state',
  'ready',
  'epoch',
  ARGV[1],
  'revision',
  ARGV[2],
  'marker_hash',
  ARGV[3],
  'activated_at_ms',
  now_ms()
)
return { 'reconciled' }
