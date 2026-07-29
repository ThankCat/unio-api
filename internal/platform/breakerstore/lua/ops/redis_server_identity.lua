local run_id, version = redis_server_identity()
if run_id == nil then return redis.error_reply('invalid Redis INFO server identity') end
return { run_id, version }
