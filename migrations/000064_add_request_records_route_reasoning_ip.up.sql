-- 请求记录富化（批二）：线路快照 + 推理强度归一 + 客户端 IP。
-- route_id 为请求创建时 API Key 绑定线路的快照：即使之后 Key 换绑线路，历史请求仍据此显示当时线路
--   （列表按 route_id JOIN routes 取名；历史行 NULL 时回落到 Key 当前绑定）。
-- reasoning_effort 为跨协议归一档位（none/minimal/low/medium/high/xhigh）：OpenAI 取 reasoning_effort，
--   Anthropic 由 thinking.budget_tokens 归一（映射见 PLAN-request-records-redesign）。
-- reasoning_budget_tokens 保留 Anthropic 原始预算（OpenAI 为 NULL）。client_ip 为客户端来源 IP（无地理）。
ALTER TABLE request_records
    ADD COLUMN route_id                BIGINT,
    ADD COLUMN reasoning_effort        TEXT,
    ADD COLUMN reasoning_budget_tokens INTEGER CHECK (reasoning_budget_tokens IS NULL OR reasoning_budget_tokens >= 0),
    ADD COLUMN client_ip               TEXT;
