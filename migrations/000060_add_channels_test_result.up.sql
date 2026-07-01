-- 为 channel 增加「最近一次主动检测结果」四列（渠道检测 / 一键测渠道，阶段一）。
-- 主动检测 = 用渠道自己的 base_url + 凭据，挑一个绑定模型发一个最小 "hi" 请求，
-- 验证「连得上 + 凭据有效 + 模型可用」，记录延迟与可读失败原因。与被动熔断/cooldown 正交。
-- 四列均可空：从未检测过时全为 NULL；仅由检测端点写入，不参与路由/计费，不改渠道启停状态。
ALTER TABLE channels
    ADD COLUMN last_tested_at       TIMESTAMPTZ,
    ADD COLUMN last_test_ok         BOOLEAN,
    ADD COLUMN last_test_latency_ms INTEGER CHECK (last_test_latency_ms IS NULL OR last_test_latency_ms >= 0),
    ADD COLUMN last_test_error      TEXT;
