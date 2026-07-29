local ep = KEYS[1]
if key_type(ep) ~= 'none' and key_type(ep) ~= 'hash' then return redis.error_reply('invalid provider key type') end
if redis.call('HGET', ep, 'control_present') == '1' then return { 'exists' } end
if
  tonumber(ARGV[1]) == nil
  or tonumber(ARGV[1]) < 1
  or tonumber(ARGV[2]) == nil
  or tonumber(ARGV[2]) < 1
  or not valid_status(ARGV[3])
then
  return redis.error_reply('invalid provider control')
end
restore_origin(ep, ARGV[1], ARGV[2], ARGV[3], now_ms())
return { 'created' }
