local control = KEYS[1]
if redis.call('EXISTS', control) == 1 then return { 'exists' } end
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
  'restored'
)
return { 'installed' }
