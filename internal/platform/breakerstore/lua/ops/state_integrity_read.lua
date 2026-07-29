local marker = KEYS[1]
if redis.call('EXISTS', marker) == 0 then return { 0, '', '', 0, '', '', '', '', '', 0, '', 0, '', '' } end
return {
  1,
  redis.call('HGET', marker, 'state') or '',
  redis.call('HGET', marker, 'epoch') or '',
  tonumber(redis.call('HGET', marker, 'revision')) or 0,
  redis.call('HGET', marker, 'marker_hash') or '',
  redis.call('HGET', marker, 'operation_token') or '',
  redis.call('HGET', marker, 'transition_hash') or '',
  redis.call('HGET', marker, 'expected_marker_hash') or '',
  redis.call('HGET', marker, 'old_epoch') or '',
  tonumber(redis.call('HGET', marker, 'old_revision')) or 0,
  redis.call('HGET', marker, 'new_epoch') or '',
  tonumber(redis.call('HGET', marker, 'new_revision')) or 0,
  redis.call('HGET', marker, 'last_operation_token') or '',
  redis.call('HGET', marker, 'last_transition_hash') or '',
}
