-- 归因维度 fault_party：把每次 attempt 的失败/取消归到「上游 / 客户端 / 平台」，
-- 使运维口径（渠道健康、成功率、最近错误）只在「上游故障」时归咎渠道，与运行时熔断器
-- IsChannelFaultError 一致（timeout/server/rate_limit/auth/permission=上游；bad_request/canceled=非渠道）。
--
-- 采用 STORED 生成列：由 status/error_code/upstream_status_code 派生，无需改写网关热路径，
-- 且新增 STORED 列会在迁移时对历史行一次性回填。仅影响只读运维聚合，不参与计费。
--   - canceled            → client（客户端取消）
--   - failed + 平台错误码  → platform（gateway_/routing_/ledger_/config_/adapter_not_registered）
--   - failed + client_canceled 码 → client
--   - failed + 上游 4xx（401/403/408/429 除外）→ client（请求本身问题，渠道正常拒绝）
--   - failed 其余（含超时/5xx/鉴权/限流/上游通信错误）→ upstream
--   - succeeded / running → NULL（无归因）
ALTER TABLE request_attempts
    ADD COLUMN fault_party text
    GENERATED ALWAYS AS (
        CASE
            WHEN status = 'canceled' THEN 'client'
            WHEN status <> 'failed' THEN NULL
            WHEN error_code LIKE 'gateway_%'
              OR error_code LIKE 'routing_%'
              OR error_code LIKE 'ledger_%'
              OR error_code LIKE 'config_%'
              OR error_code = 'adapter_not_registered' THEN 'platform'
            WHEN error_code = 'client_canceled' THEN 'client'
            WHEN upstream_status_code BETWEEN 400 AND 499
              AND upstream_status_code NOT IN (401, 403, 408, 429) THEN 'client'
            ELSE 'upstream'
        END
    ) STORED;

-- 运维聚合常按 (channel_id, status) / (provider_id, status) 过滤后再按 fault_party 归因。
CREATE INDEX IF NOT EXISTS idx_request_attempts_channel_fault
    ON request_attempts (channel_id, fault_party)
    WHERE status = 'failed';
