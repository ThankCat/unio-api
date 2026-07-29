local control = KEYS[1]
local revision = tonumber(ARGV[1])
if revision == nil or revision < 1 or ARGV[2] == '' or ARGV[3] == '' then
  return redis.error_reply('invalid authoritative control')
end
local typ = redis.call('TYPE', control)
if type(typ) == 'table' then typ = typ['ok'] end
local unchanged = typ == 'hash'
  and redis.call('HGET', control, 'active_revision') == ARGV[1]
  and redis.call('HGET', control, 'active_payload_hash') == ARGV[2]
  and redis.call('HGET', control, 'active_payload') == ARGV[3]
  and redis.call('HEXISTS', control, 'pending_revision') == 0
  and redis.call('HEXISTS', control, 'pending_payload_hash') == 0
  and redis.call('HEXISTS', control, 'pending_payload') == 0
  and redis.call('HEXISTS', control, 'pending_op_token') == 0
if unchanged then return { 'unchanged' } end
redis.call('DEL', control)
redis.call(
  'HSET',
  control,
  'active_revision',
  ARGV[1],
  'active_payload_hash',
  ARGV[2],
  'active_payload',
  ARGV[3],
  'last_terminal',
  'reconciled'
)
return { 'reconciled' }
