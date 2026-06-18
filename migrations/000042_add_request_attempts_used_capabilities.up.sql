-- 能力自动校正按 key 精确命中埋点（DESIGN-capability-autocalibration TASK-H）。
--
-- request_attempts.used_capabilities：本次成功响应被 adapter 解析「真正用到」的能力 key
-- （如响应里出现 function_call → tools.function）。它取代 finish_class 作为 tools.* 的强证据来源：
--   - finish_class=tool_use 只能笼统证明「某个工具被调了」，无法区分 function/custom；
--   - 且 OpenAI Responses 直传时 finish_class 恒为 stop（Codex 主力流量），tools.* 永远拿不到证据。
-- 校正聚合按 key 命中归因；无埋点的旧行 / 其它 adapter 仍回退到 finish_class（粗粒度）。
ALTER TABLE request_attempts
    ADD COLUMN used_capabilities TEXT[] NOT NULL DEFAULT '{}';

-- settlement_recovery_jobs.used_capabilities：流式风险路径的 settlement 由 worker 重放时，
-- 需把命中埋点随 job 持久化，重放写回 request_attempts.used_capabilities，避免 recovery 路径丢证据。
ALTER TABLE settlement_recovery_jobs
    ADD COLUMN used_capabilities TEXT[] NOT NULL DEFAULT '{}';
