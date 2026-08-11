-- Provider 消费流水显式记录成本依据；已经形成非零成本快照的请求统一进入账本。
ALTER TABLE public.provider_ledger_entries
    ADD COLUMN usage_source text;

UPDATE public.provider_ledger_entries e
SET usage_source = u.usage_source
FROM public.usage_records u
WHERE e.entry_type = 'usage_debit'
  AND u.request_record_id = e.request_record_id;

UPDATE public.provider_ledger_entries e
SET usage_source = p.usage_source
FROM public.provider_probe_records p
WHERE e.entry_type = 'probe_debit'
  AND p.id = e.provider_probe_record_id;

ALTER TABLE public.provider_ledger_entries
    DROP CONSTRAINT provider_ledger_entries_source_check;

ALTER TABLE public.provider_ledger_entries
    ADD CONSTRAINT provider_ledger_entries_usage_source_check CHECK (
        usage_source IS NULL
        OR usage_source IN ('upstream_response', 'upstream_stream', 'partial_stream_estimate')
    ),
    ADD CONSTRAINT provider_ledger_entries_source_check CHECK (
        (
            entry_type = 'usage_debit'
            AND provider_probe_record_id IS NULL
            AND request_record_id IS NOT NULL
            AND request_attempt_id IS NOT NULL
            AND cost_snapshot_id IS NOT NULL
            AND channel_id IS NOT NULL
            AND request_id IS NOT NULL AND btrim(request_id) <> ''
            AND channel_name IS NOT NULL AND btrim(channel_name) <> ''
            AND upstream_model IS NOT NULL AND btrim(upstream_model) <> ''
            AND usage_source IN ('upstream_response', 'upstream_stream', 'partial_stream_estimate')
        )
        OR (
            entry_type = 'probe_debit'
            AND provider_probe_record_id IS NOT NULL
            AND request_record_id IS NULL
            AND request_attempt_id IS NULL
            AND cost_snapshot_id IS NULL
            AND channel_id IS NULL
            AND request_id IS NULL
            AND channel_name IS NULL
            AND upstream_model IS NULL
            AND usage_source IN ('upstream_response', 'upstream_stream')
        )
        OR (
            entry_type IN ('adjustment_credit', 'adjustment_debit')
            AND provider_probe_record_id IS NULL
            AND request_record_id IS NULL
            AND request_attempt_id IS NULL
            AND cost_snapshot_id IS NULL
            AND channel_id IS NULL
            AND request_id IS NULL
            AND channel_name IS NULL
            AND upstream_model IS NULL
            AND usage_source IS NULL
        )
    );

-- usage 是否可靠与价格能否解析是两个独立事实；只有成本三元组完整时才会写 probe_debit。
UPDATE public.provider_probe_records
SET currency = NULL,
    formula_version = NULL
WHERE cost_amount IS NULL;

ALTER TABLE public.provider_probe_records
    DROP CONSTRAINT provider_probe_records_usage_check;

ALTER TABLE public.provider_probe_records
    ADD CONSTRAINT provider_probe_records_usage_check CHECK (
        usage_reliable = false
        OR (usage_source IS NOT NULL AND usage_facts IS NOT NULL)
    ),
    ADD CONSTRAINT provider_probe_records_cost_details_check CHECK (
        (cost_amount IS NULL AND currency IS NULL AND formula_version IS NULL)
        OR (
            cost_amount IS NOT NULL
            AND currency IS NOT NULL AND btrim(currency) <> ''
            AND formula_version IS NOT NULL AND btrim(formula_version) <> ''
        )
    );

-- 历史待对账事实不补扣；移除旧模型后由运营按上游真实余额做一次目标余额校准。
DROP TABLE public.provider_cost_risks;
DROP TABLE public.channel_cost_exposures;
ALTER TABLE public.channels DROP COLUMN upstream_bills_on_disconnect;
