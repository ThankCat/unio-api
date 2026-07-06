-- 渠道检测日志新增「上游原始错误」列：失败时记录上游返回的错误响应体截断快照（约 2KB 上限）。
-- error_code/message 是归类后的稳定码与可读中文原因；upstream_error 保留上游原文，供排障时看到完整错误。
-- 可空：成功、无响应体（连不上/超时）或未捕获时为 NULL。
ALTER TABLE channel_test_logs
    ADD COLUMN upstream_error TEXT;
