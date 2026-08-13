-- Route-Model Offering 是线路向客户承诺的模型产品范围（ADR-0018）。
-- Offering 持久保存管理员的售卖选择：enabled 表示当前售卖，disabled 保留历史关系与停用原因，不物理删除。
-- breaker、容量、凭据等瞬时运行态不改写本表；结构支撑 = Route 池内同协议、enabled Channel 承载的 enabled Binding。
CREATE TABLE public.route_model_offerings (
    route_id bigint NOT NULL,
    model_id bigint NOT NULL,
    ingress_protocol text NOT NULL,
    status text DEFAULT 'enabled'::text NOT NULL,
    disabled_reason text,
    disabled_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT route_model_offerings_pkey PRIMARY KEY (route_id, model_id, ingress_protocol),
    CONSTRAINT route_model_offerings_protocol_check CHECK (ingress_protocol = ANY (ARRAY['openai'::text, 'anthropic'::text])),
    CONSTRAINT route_model_offerings_status_check CHECK (status = ANY (ARRAY['enabled'::text, 'disabled'::text])),
    CONSTRAINT route_model_offerings_disabled_reason_check CHECK (
        disabled_reason IS NULL
        OR disabled_reason = ANY (ARRAY[
            'manual_unselected'::text,
            'model_disabled'::text,
            'binding_disabled'::text,
            'channel_disabled'::text,
            'route_channel_removed'::text,
            'channel_protocol_changed'::text,
            'migration_backfill'::text
        ])
    ),
    -- enabled 行必须无停用原因与时间；disabled 行必须两者齐备（ADR-0018：重新启用时清空原因和时间）。
    CONSTRAINT route_model_offerings_disabled_fields_check CHECK (
        (status = 'enabled' AND disabled_reason IS NULL AND disabled_at IS NULL)
        OR (status = 'disabled' AND disabled_reason IS NOT NULL AND disabled_at IS NOT NULL)
    ),
    CONSTRAINT route_model_offerings_route_id_fkey FOREIGN KEY (route_id) REFERENCES public.routes(id) ON DELETE CASCADE,
    CONSTRAINT route_model_offerings_model_id_fkey FOREIGN KEY (model_id) REFERENCES public.models(id) ON DELETE CASCADE
);

CREATE INDEX idx_route_model_offerings_model ON public.route_model_offerings USING btree (model_id);

-- 一次切换回填（ADR-0018「数据与迁移要求」）。历史 Route 没有显式产品关系：
-- 先按当前渠道池的绑定事实生成 Offering（含 disabled Channel/Binding，保留既有产品承诺），
-- 再修复不变量并按最终语义收敛状态，最后以 NOTICE 输出各类变更数量作为迁移报告。
DO $$
DECLARE
    seeded_count bigint := 0;
    binding_fixed_count bigint := 0;
    backfill_disabled_count bigint := 0;
BEGIN
    -- Seed：为既有 Route 生成 enabled Offering（模型必须全局启用；协议取渠道 protocol）。
    INSERT INTO public.route_model_offerings (route_id, model_id, ingress_protocol)
    SELECT DISTINCT rc.route_id, cm.model_id, c.protocol
    FROM public.route_channels rc
    JOIN public.channels c ON c.id = rc.channel_id
    JOIN public.channel_models cm ON cm.channel_id = c.id
    JOIN public.models m ON m.id = cm.model_id AND m.status = 'enabled'
    WHERE c.protocol IN ('openai', 'anthropic')
    ON CONFLICT DO NOTHING;
    GET DIAGNOSTICS seeded_count = ROW_COUNT;

    -- 不变量修复（ADR-0018 回填第 2 步）：disabled Model 下不得存在 enabled Binding。
    UPDATE public.channel_models cm
    SET status = 'disabled', updated_at = now()
    FROM public.models m
    WHERE m.id = cm.model_id AND m.status = 'disabled' AND cm.status = 'enabled';
    GET DIAGNOSTICS binding_fixed_count = ROW_COUNT;

    -- 回填第 3 步：没有「enabled Channel 承载的同协议 enabled Binding」结构支撑的 Offering 置 disabled。
    -- 迁移无法追溯历史触发操作，统一记 migration_backfill。
    UPDATE public.route_model_offerings o
    SET status = 'disabled', disabled_reason = 'migration_backfill', disabled_at = now(), updated_at = now()
    WHERE o.status = 'enabled'
      AND NOT EXISTS (
          SELECT 1
          FROM public.route_channels rc
          JOIN public.channels c ON c.id = rc.channel_id
          JOIN public.channel_models cm ON cm.channel_id = c.id
          WHERE rc.route_id = o.route_id
            AND c.status = 'enabled'
            AND c.protocol = o.ingress_protocol
            AND cm.model_id = o.model_id
            AND cm.status = 'enabled'
      );
    GET DIAGNOSTICS backfill_disabled_count = ROW_COUNT;

    RAISE NOTICE 'route_model_offerings backfill report: seeded=%, bindings_disabled_under_disabled_models=%, offerings_disabled_missing_support=%',
        seeded_count, binding_fixed_count, backfill_disabled_count;
END
$$;
