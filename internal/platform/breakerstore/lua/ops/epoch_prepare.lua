local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end

local marker = KEYS[1]
local op = KEYS[2]
local token = ARGV[1]
local old_epoch = ARGV[2]
local old_revision = ARGV[3]
local new_epoch = ARGV[4]
local new_revision = ARGV[5]
local transition_hash = ARGV[6]
local expected_hash = ARGV[7]
local new_ready_hash = ARGV[8]
local now = now_ms()

local function op_compatible(allowed_state)
  if redis.call('EXISTS', op) == 0 then return true end
  if
    redis.call('HGET', op, 'token') ~= token
    or redis.call('HGET', op, 'transition_hash') ~= transition_hash
    or redis.call('HGET', op, 'new_epoch') ~= new_epoch
    or redis.call('HGET', op, 'new_revision') ~= new_revision
  then
    return false
  end
  return redis.call('HGET', op, 'state') == allowed_state
end

local function write_pending()
  redis.call(
    'HSET',
    marker,
    'state',
    'pending',
    'operation_token',
    token,
    'transition_hash',
    transition_hash,
    'expected_marker_hash',
    expected_hash,
    'old_epoch',
    old_epoch,
    'old_revision',
    old_revision,
    'new_epoch',
    new_epoch,
    'new_revision',
    new_revision,
    'pending_at_ms',
    now
  )
  redis.call('HDEL', marker, 'marker_hash', 'last_operation_token', 'last_transition_hash')
  if redis.call('HGET', marker, 'epoch') == false then
    redis.call('HSET', marker, 'epoch', new_epoch, 'revision', new_revision, 'initialized_at_ms', now)
  end
  redis.call(
    'HSET',
    op,
    'token',
    token,
    'transition_hash',
    transition_hash,
    'expected_marker_hash',
    expected_hash,
    'old_epoch',
    old_epoch,
    'old_revision',
    old_revision,
    'new_epoch',
    new_epoch,
    'new_revision',
    new_revision,
    'state',
    'pending',
    'updated_at_ms',
    now
  )
  return { 'prepared' }
end

-- 1) marker absent：只有 durable expected=absent 可以建立 pending。
if redis.call('EXISTS', marker) == 0 then
  if expected_hash ~= 'absent' or not op_compatible('pending') then return { 'conflict' } end
  return write_pending()
end

local state = redis.call('HGET', marker, 'state') or ''
local cur_epoch = redis.call('HGET', marker, 'epoch') or ''
local cur_revision = redis.call('HGET', marker, 'revision') or ''

-- 2) durable old ready：必须同时匹配 old epoch/revision 与精确 canonical hash。
if
  state == 'ready'
  and cur_epoch == old_epoch
  and cur_revision == old_revision
  and expected_hash ~= 'absent'
  and redis.call('HGET', marker, 'marker_hash') == expected_hash
then
  if not op_compatible('pending') then return { 'conflict' } end
  return write_pending()
end

-- 3) 同 operation pending：幂等，op key 丢失时按 marker 重建。
if
  state == 'pending'
  and redis.call('HGET', marker, 'operation_token') == token
  and redis.call('HGET', marker, 'transition_hash') == transition_hash
  and redis.call('HGET', marker, 'expected_marker_hash') == expected_hash
  and redis.call('HGET', marker, 'old_epoch') == old_epoch
  and redis.call('HGET', marker, 'old_revision') == old_revision
  and redis.call('HGET', marker, 'new_epoch') == new_epoch
  and redis.call('HGET', marker, 'new_revision') == new_revision
then
  if redis.call('EXISTS', op) == 1 and not op_compatible('pending') then return { 'conflict' } end
  redis.call(
    'HSET',
    op,
    'token',
    token,
    'transition_hash',
    transition_hash,
    'expected_marker_hash',
    expected_hash,
    'old_epoch',
    old_epoch,
    'old_revision',
    old_revision,
    'new_epoch',
    new_epoch,
    'new_revision',
    new_revision,
    'state',
    'pending',
    'updated_at_ms',
    now
  )
  return { 'prepared' }
end

-- 4) 同 operation new ready：只报告观测，是否可终结 PostgreSQL 由 application 根据 db_committed 决定。
if
  state == 'ready'
  and cur_epoch == new_epoch
  and cur_revision == new_revision
  and redis.call('HGET', marker, 'marker_hash') == new_ready_hash
  and redis.call('HGET', marker, 'last_operation_token') == token
  and redis.call('HGET', marker, 'last_transition_hash') == transition_hash
then
  if redis.call('EXISTS', op) == 1 and not op_compatible('committed') then return { 'conflict' } end
  redis.call(
    'HSET',
    op,
    'token',
    token,
    'transition_hash',
    transition_hash,
    'expected_marker_hash',
    expected_hash,
    'old_epoch',
    old_epoch,
    'old_revision',
    old_revision,
    'new_epoch',
    new_epoch,
    'new_revision',
    new_revision,
    'state',
    'committed',
    'updated_at_ms',
    now
  )
  return { 'new_ready_observed' }
end

-- 5) 其它 marker/op：零覆盖 conflict。
return { 'conflict' }
