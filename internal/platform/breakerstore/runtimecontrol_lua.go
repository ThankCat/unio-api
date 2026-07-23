package breakerstore

// 通用 runtime-control 状态机 Lua（§5.3.16）。同一套 Prepare/Commit/Abort/Recover/Read 供：
//   - admission 线路/渠道默认限流与共享 concurrency 默认
//   - admission Channel override
//   - gateway.circuit_breaker / gateway.routing_balance
// 复用，不另建第二张状态机。Redis 侧只记录 prepared|committed|aborted 终态；PostgreSQL 侧单独记录
// preparing|prepared|db_committed|committed|aborted（由 application 编排，见 Go 封装）。
//
// 控制 hash 字段：active_revision/active_payload_hash/active_payload、
// pending_revision/pending_payload_hash/pending_payload/pending_op_token、last_terminal。
// op key（独立）字段：token/payload_hash/next_revision/state/created_at_ms。

// luaControlPrepare 建立 pending：校验 next=current+1、active_revision==current、无其它 pending。
// KEYS[1]=control, KEYS[2]=op。ARGV: token, current_revision, next_revision, payload_hash, payload, op_ttl_ms。
// 返回 {'prepared'|'committed'|'aborted'|'invalid'|'stale',active|'conflict'|'conflict_pending'}。
const luaControlPrepare = `
local control = KEYS[1]
local op = KEYS[2]
local token = ARGV[1]
local current_rev = tonumber(ARGV[2])
local next_rev = tonumber(ARGV[3])
local payload_hash = ARGV[4]
local payload = ARGV[5]

-- 幂等/冲突：op 已存在。
if redis.call('EXISTS', op) == 1 then
  local otoken = redis.call('HGET', op, 'token')
  local ohash = redis.call('HGET', op, 'payload_hash')
  if otoken == token and ohash == payload_hash then
    return {redis.call('HGET', op, 'state')}
  end
  return {'conflict'}
end

if next_rev ~= current_rev + 1 then
  return {'invalid'}
end

local active_rev = tonumber(redis.call('HGET', control, 'active_revision')) or 0
if active_rev ~= current_rev then
  return {'stale', active_rev}
end
local pending_rev = tonumber(redis.call('HGET', control, 'pending_revision')) or 0
if pending_rev ~= 0 then
  return {'conflict_pending'}
end

redis.call('HSET', control,
  'pending_revision', next_rev,
  'pending_payload_hash', payload_hash,
  'pending_payload', payload,
  'pending_op_token', token)
redis.call('HSET', op, 'token', token, 'payload_hash', payload_hash,
  'next_revision', next_rev, 'state', 'prepared')
return {'prepared'}
`

// luaControlCommit 激活 pending：pending 归属该 op 时 active<-pending 并清 pending。
// KEYS[1]=control, KEYS[2]=op。ARGV: token, payload_hash, op_ttl_ms。
// 返回 {'committed'|'unknown_op'|'aborted_conflict'|'conflict', active_revision}。
const luaControlCommit = `
local control = KEYS[1]
local op = KEYS[2]
local token = ARGV[1]
local payload_hash = ARGV[2]
local op_ttl_ms = tonumber(ARGV[3])

if redis.call('EXISTS', op) == 0 then return {'unknown_op'} end
local state = redis.call('HGET', op, 'state')
if state == 'committed' then return {'committed', tonumber(redis.call('HGET', control, 'active_revision')) or 0} end
if state == 'aborted' then return {'aborted_conflict'} end
if redis.call('HGET', op, 'token') ~= token or redis.call('HGET', op, 'payload_hash') ~= payload_hash then
  return {'conflict'}
end
if redis.call('HGET', control, 'pending_op_token') ~= token or redis.call('HGET', control, 'pending_payload_hash') ~= payload_hash then
  return {'conflict'}
end

local next_rev = tonumber(redis.call('HGET', control, 'pending_revision'))
redis.call('HSET', control,
  'active_revision', next_rev,
  'active_payload_hash', redis.call('HGET', control, 'pending_payload_hash'),
  'active_payload', redis.call('HGET', control, 'pending_payload'),
  'last_terminal', 'committed')
redis.call('HDEL', control, 'pending_revision', 'pending_payload_hash', 'pending_payload', 'pending_op_token')
redis.call('HSET', op, 'state', 'committed')
if op_ttl_ms > 0 then redis.call('PEXPIRE', op, op_ttl_ms) end
return {'committed', next_rev}
`

// luaControlAbort 撤销未提交 pending：pending 归属该 op 时清 pending。已 committed 不可 abort。
// KEYS[1]=control, KEYS[2]=op。ARGV: token, payload_hash, op_ttl_ms。返回 {'aborted'|'unknown_op'|'committed_conflict'|'conflict'}。
const luaControlAbort = `
local control = KEYS[1]
local op = KEYS[2]
local token = ARGV[1]
local payload_hash = ARGV[2]
local op_ttl_ms = tonumber(ARGV[3])

if redis.call('EXISTS', op) == 0 then return {'unknown_op'} end
local state = redis.call('HGET', op, 'state')
if state == 'aborted' then return {'aborted'} end
if state == 'committed' then return {'committed_conflict'} end
if redis.call('HGET', op, 'token') ~= token or redis.call('HGET', op, 'payload_hash') ~= payload_hash then
  return {'conflict'}
end
if redis.call('HGET', control, 'pending_op_token') == token then
  redis.call('HDEL', control, 'pending_revision', 'pending_payload_hash', 'pending_payload', 'pending_op_token')
  redis.call('HSET', control, 'last_terminal', 'aborted')
end
redis.call('HSET', op, 'state', 'aborted')
if op_ttl_ms > 0 then redis.call('PEXPIRE', op, op_ttl_ms) end
return {'aborted'}
`

// luaControlRead 只读控制态：返回 active/pending revision、两类 payload、同步状态。
// KEYS[1]=control。ARGV: expected_revision(可空 ”)。
// 返回 {active_rev, pending_rev, active_payload, pending_payload, sync_state}。
const luaControlRead = `
local control = KEYS[1]
if redis.call('EXISTS', control) == 0 then
  return {0, 0, '', '', 'absent'}
end
local active_rev = tonumber(redis.call('HGET', control, 'active_revision')) or 0
local pending_rev = tonumber(redis.call('HGET', control, 'pending_revision')) or 0
local payload = redis.call('HGET', control, 'active_payload') or ''
local pending_payload = redis.call('HGET', control, 'pending_payload') or ''
local sync = 'active'
if pending_rev ~= 0 then sync = 'pending' end
if ARGV[1] ~= '' then
  local expected = tonumber(ARGV[1])
  if pending_rev ~= 0 then
    sync = 'pending'
  elseif active_rev < expected then
    sync = 'stale'
  elseif active_rev > expected then
    sync = 'ahead'
  end
end
return {active_rev, pending_rev, payload, pending_payload, sync}
`

// luaControlRestoreMissing 仅当控制缺失时安装 PostgreSQL 当前任意 revision 的 active（recovery-only，§5.3.18）。
// 已存在控制绝不覆盖。KEYS[1]=control。ARGV: revision, payload_hash, payload。返回 {'installed'|'exists'}。
const luaControlRestoreMissing = `
local control = KEYS[1]
if redis.call('EXISTS', control) == 1 then return {'exists'} end
redis.call('HSET', control,
  'active_revision', ARGV[1],
  'active_payload_hash', ARGV[2],
  'active_payload', ARGV[3],
  'last_terminal', 'restored')
return {'installed'}
`

// luaControlRecoverCommitted 仅供 reconciler 在 PostgreSQL operation=db_committed 时使用。
// PostgreSQL 新 revision/payload 已是持久事实，因此 Redis control/op 缺失或仍停在 current 时可原子恢复到 next；
// 已有其它 revision 或冲突 pending 时绝不覆盖。KEYS: control, op。
// ARGV: token,current_revision,next_revision,payload_hash,payload,op_ttl_ms。
const luaControlRecoverCommitted = `
local control = KEYS[1]
local op = KEYS[2]
local token = ARGV[1]
local current_rev = tonumber(ARGV[2])
local next_rev = tonumber(ARGV[3])
local payload_hash = ARGV[4]
local payload = ARGV[5]
local op_ttl_ms = tonumber(ARGV[6])

if next_rev ~= current_rev + 1 then return {'invalid'} end

if redis.call('EXISTS', op) == 1 then
  local otoken = redis.call('HGET', op, 'token')
  local ohash = redis.call('HGET', op, 'payload_hash')
  local ostate = redis.call('HGET', op, 'state')
  if otoken ~= token or ohash ~= payload_hash then return {'conflict'} end
  if ostate == 'aborted' then return {'aborted_conflict'} end
end

local exists = redis.call('EXISTS', control)
local active_rev = tonumber(redis.call('HGET', control, 'active_revision')) or 0
local active_hash = redis.call('HGET', control, 'active_payload_hash') or ''
local pending_rev = tonumber(redis.call('HGET', control, 'pending_revision')) or 0

if exists == 1 and active_rev ~= current_rev and active_rev ~= next_rev then
  return {'stale', active_rev}
end
if active_rev == next_rev and active_hash ~= payload_hash then
  return {'conflict'}
end
if pending_rev ~= 0 then
  if pending_rev ~= next_rev or redis.call('HGET', control, 'pending_op_token') ~= token
      or redis.call('HGET', control, 'pending_payload_hash') ~= payload_hash then
    return {'conflict_pending'}
  end
end

redis.call('HSET', control,
  'active_revision', next_rev,
  'active_payload_hash', payload_hash,
  'active_payload', payload,
  'last_terminal', 'committed')
redis.call('HDEL', control, 'pending_revision', 'pending_payload_hash', 'pending_payload', 'pending_op_token')
redis.call('HSET', op, 'token', token, 'payload_hash', payload_hash,
  'next_revision', next_rev, 'state', 'committed')
if op_ttl_ms > 0 then redis.call('PEXPIRE', op, op_ttl_ms) end
return {'committed', next_rev}
`

// luaControlRecoverAborted 仅供 reconciler 在 PostgreSQL operation=preparing|prepared 且业务 revision 未提交时使用。
// 它按 PostgreSQL 旧 active 事实恢复缺失 control，并只清理属于同 token/hash 的 pending；冲突状态绝不覆盖。
// KEYS: control, op。ARGV: token,current_revision,next_revision,pending_payload_hash,
// current_payload_hash,current_payload,op_ttl_ms。
const luaControlRecoverAborted = `
local control = KEYS[1]
local op = KEYS[2]
local token = ARGV[1]
local current_rev = tonumber(ARGV[2])
local next_rev = tonumber(ARGV[3])
local pending_hash = ARGV[4]
local current_hash = ARGV[5]
local current_payload = ARGV[6]
local op_ttl_ms = tonumber(ARGV[7])

if next_rev ~= current_rev + 1 then return {'invalid'} end

if redis.call('EXISTS', op) == 1 then
  local otoken = redis.call('HGET', op, 'token')
  local ohash = redis.call('HGET', op, 'payload_hash')
  local ostate = redis.call('HGET', op, 'state')
  if otoken ~= token or ohash ~= pending_hash then return {'conflict'} end
  if ostate == 'committed' then return {'committed_conflict'} end
end

if redis.call('EXISTS', control) == 0 then
  redis.call('HSET', control,
    'active_revision', current_rev,
    'active_payload_hash', current_hash,
    'active_payload', current_payload,
    'last_terminal', 'aborted')
else
  local active_rev = tonumber(redis.call('HGET', control, 'active_revision')) or 0
  local active_hash = redis.call('HGET', control, 'active_payload_hash') or ''
  if active_rev ~= current_rev or active_hash ~= current_hash then
    return {'stale', active_rev}
  end
end

local pending_rev = tonumber(redis.call('HGET', control, 'pending_revision')) or 0
if pending_rev ~= 0 then
  if pending_rev ~= next_rev or redis.call('HGET', control, 'pending_op_token') ~= token
      or redis.call('HGET', control, 'pending_payload_hash') ~= pending_hash then
    return {'conflict_pending'}
  end
  redis.call('HDEL', control, 'pending_revision', 'pending_payload_hash', 'pending_payload', 'pending_op_token')
end

redis.call('HSET', control, 'last_terminal', 'aborted')
redis.call('HSET', op, 'token', token, 'payload_hash', pending_hash,
  'next_revision', next_rev, 'state', 'aborted')
if op_ttl_ms > 0 then redis.call('PEXPIRE', op, op_ttl_ms) end
return {'aborted', current_rev}
`
