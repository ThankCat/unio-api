-- 以模型为中心的供给与定价：供给的根从 Route 换成 Model。
--
-- 本迁移做四件事：
--   1. 渠道支持多协议（protocol → protocols 数组），一条渠道可同时服务 openai 与 anthropic；
--   2. 入口限流从线路级迁到用户级；
--   3. 模型可直接定绝对售价，缺省回退「基准价 × 全局售价倍率」；
--   4. 删除 route 三表与 user_model_policies，以及各表上的 route_id 外键列。
--
-- 毛利守卫的重建在 000053，必须紧随本迁移执行：本迁移会先卸掉旧守卫。

-- ── 0. 先卸掉依赖 routes 的毛利守卫（000053 重建） ──────────────────────────
DROP TRIGGER IF EXISTS trg_routes_margin_guard ON routes;
DROP TRIGGER IF EXISTS trg_route_channels_margin_guard ON route_channels;
DROP TRIGGER IF EXISTS trg_models_margin_guard ON models;
DROP TRIGGER IF EXISTS trg_channels_margin_guard ON channels;
DROP TRIGGER IF EXISTS trg_providers_margin_guard ON providers;
DROP TRIGGER IF EXISTS trg_channel_models_margin_guard ON channel_models;
DROP TRIGGER IF EXISTS trg_model_prices_margin_guard ON model_prices;
DROP TRIGGER IF EXISTS trg_channel_prices_margin_guard ON channel_prices;
DROP TRIGGER IF EXISTS trg_channel_cost_multipliers_margin_guard ON channel_cost_multipliers;
DROP TRIGGER IF EXISTS trg_channel_recharge_factors_margin_guard ON channel_recharge_factors;
DROP FUNCTION IF EXISTS public.assert_non_negative_route_margins();

-- ── 1. 渠道多协议 ────────────────────────────────────────────────────────────
-- protocols 决定这条渠道能接哪些入口协议族；adapter_key 决定用哪套上游方言。
-- 二者正交：(protocol, adapter_key) 的每个组合都必须在代码注册表中存在，
-- 由 admin 写入路径校验（DB 不做枚举约束，注册表是代码事实）。
ALTER TABLE channels ADD COLUMN protocols TEXT[] NOT NULL DEFAULT '{}';
UPDATE channels SET protocols = ARRAY[protocol];
ALTER TABLE channels ALTER COLUMN protocols DROP DEFAULT;
ALTER TABLE channels ADD CONSTRAINT ck_channels_protocols_non_empty
    CHECK (cardinality(protocols) > 0);
ALTER TABLE channels ADD CONSTRAINT ck_channels_protocols_known
    CHECK (protocols <@ ARRAY['openai', 'anthropic']::text[]);
ALTER TABLE channels DROP COLUMN protocol;

COMMENT ON COLUMN channels.protocols IS
    '本渠道可服务的入口协议族集合；与 adapter_key 组合后必须在代码 adapter 注册表中存在。';

-- ── 2. 入口限流迁到用户级 ────────────────────────────────────────────────────
-- 语义沿用线路级：NULL=继承全局默认，0=显式不限，>0=上限。
-- 计数主体同时也从 (route,user) 变为 (user)，Redis key 由应用侧改写。
ALTER TABLE users
    ADD COLUMN rpm_limit INTEGER,
    ADD COLUMN rpd_limit INTEGER,
    ADD COLUMN concurrency_limit INTEGER;

ALTER TABLE users
    ADD CONSTRAINT ck_users_rpm_limit_non_negative
        CHECK (rpm_limit IS NULL OR rpm_limit >= 0),
    ADD CONSTRAINT ck_users_rpd_limit_non_negative
        CHECK (rpd_limit IS NULL OR rpd_limit >= 0),
    ADD CONSTRAINT ck_users_concurrency_limit_non_negative
        CHECK (concurrency_limit IS NULL OR concurrency_limit >= 0);

-- 继承原线路上的限流：仅有单条线路时等价于原语义。
UPDATE users u
SET rpm_limit = r.rpm_limit,
    rpd_limit = r.rpd_limit,
    concurrency_limit = r.concurrency_limit
FROM (
    SELECT rt.rpm_limit, rt.rpd_limit, rt.concurrency_limit
    FROM routes rt
    WHERE rt.status = 'enabled'
    ORDER BY rt.id
    LIMIT 1
) r
WHERE r.rpm_limit IS NOT NULL
   OR r.rpd_limit IS NOT NULL
   OR r.concurrency_limit IS NOT NULL;

COMMENT ON COLUMN users.rpm_limit IS
    '用户级每分钟请求上限；NULL 继承全局默认，0 表示不限。同一用户的多把 Key 共享该配额。';

-- ── 3. 模型级售价 ────────────────────────────────────────────────────────────
-- 与成本侧对称的两级解析：
--   客户售价 = 绝对售价（本表 sale_* 非空时） 或 基准价 × 全局售价倍率
--   渠道成本 = channel_prices 绝对覆盖  或  基准价 × 成本倍率 × 充值倍率
-- sale_* 与基准价共享同一时间窗行，改售价与改基准价一样通过新开窗口完成。
ALTER TABLE model_prices
    ADD COLUMN sale_uncached_input_price NUMERIC(20, 10),
    ADD COLUMN sale_cache_read_input_price NUMERIC(20, 10),
    ADD COLUMN sale_cache_write_5m_input_price NUMERIC(20, 10),
    ADD COLUMN sale_cache_write_1h_input_price NUMERIC(20, 10),
    ADD COLUMN sale_cache_write_30m_input_price NUMERIC(20, 10),
    ADD COLUMN sale_output_price NUMERIC(20, 10),
    ADD COLUMN sale_reasoning_output_price NUMERIC(20, 10);

ALTER TABLE model_prices
    ADD CONSTRAINT ck_model_prices_sale_non_negative CHECK (
        (sale_uncached_input_price IS NULL OR sale_uncached_input_price >= 0)
        AND (sale_cache_read_input_price IS NULL OR sale_cache_read_input_price >= 0)
        AND (sale_cache_write_5m_input_price IS NULL OR sale_cache_write_5m_input_price >= 0)
        AND (sale_cache_write_1h_input_price IS NULL OR sale_cache_write_1h_input_price >= 0)
        AND (sale_cache_write_30m_input_price IS NULL OR sale_cache_write_30m_input_price >= 0)
        AND (sale_output_price IS NULL OR sale_output_price >= 0)
        AND (sale_reasoning_output_price IS NULL OR sale_reasoning_output_price >= 0)
    );

-- 绝对售价必须整组给齐或整组留空：只填一半会让「该分项用绝对价、其余用倍率」
-- 这种混合语义进入计费，难以解释也难以校验。
ALTER TABLE model_prices
    ADD CONSTRAINT ck_model_prices_sale_all_or_none CHECK (
        (
            sale_uncached_input_price IS NULL
            AND sale_cache_read_input_price IS NULL
            AND sale_cache_write_5m_input_price IS NULL
            AND sale_cache_write_1h_input_price IS NULL
            AND sale_cache_write_30m_input_price IS NULL
            AND sale_output_price IS NULL
            AND sale_reasoning_output_price IS NULL
        )
        OR (
            sale_uncached_input_price IS NOT NULL
            AND sale_output_price IS NOT NULL
        )
    );

COMMENT ON COLUMN model_prices.sale_uncached_input_price IS
    '模型对外绝对售价；整组为空时回退「基准价 × 全局售价倍率」。可选分项为空按 billing fallback 归一。';

-- Fast 档（model_price_service_tiers）自带一套基准价，售价也必须能独立定：
-- 否则 Fast 档只能跟着标准档的倍率走，无法表达「快速通道单独加价」。
ALTER TABLE model_price_service_tiers
    ADD COLUMN sale_uncached_input_price NUMERIC(20, 10),
    ADD COLUMN sale_cache_read_input_price NUMERIC(20, 10),
    ADD COLUMN sale_cache_write_5m_input_price NUMERIC(20, 10),
    ADD COLUMN sale_cache_write_1h_input_price NUMERIC(20, 10),
    ADD COLUMN sale_cache_write_30m_input_price NUMERIC(20, 10),
    ADD COLUMN sale_output_price NUMERIC(20, 10),
    ADD COLUMN sale_reasoning_output_price NUMERIC(20, 10);

ALTER TABLE model_price_service_tiers
    ADD CONSTRAINT ck_model_price_tiers_sale_non_negative CHECK (
        (sale_uncached_input_price IS NULL OR sale_uncached_input_price >= 0)
        AND (sale_cache_read_input_price IS NULL OR sale_cache_read_input_price >= 0)
        AND (sale_cache_write_5m_input_price IS NULL OR sale_cache_write_5m_input_price >= 0)
        AND (sale_cache_write_1h_input_price IS NULL OR sale_cache_write_1h_input_price >= 0)
        AND (sale_cache_write_30m_input_price IS NULL OR sale_cache_write_30m_input_price >= 0)
        AND (sale_output_price IS NULL OR sale_output_price >= 0)
        AND (sale_reasoning_output_price IS NULL OR sale_reasoning_output_price >= 0)
    );

ALTER TABLE model_price_service_tiers
    ADD CONSTRAINT ck_model_price_tiers_sale_all_or_none CHECK (
        (
            sale_uncached_input_price IS NULL
            AND sale_cache_read_input_price IS NULL
            AND sale_cache_write_5m_input_price IS NULL
            AND sale_cache_write_1h_input_price IS NULL
            AND sale_cache_write_30m_input_price IS NULL
            AND sale_output_price IS NULL
            AND sale_reasoning_output_price IS NULL
        )
        OR (
            sale_uncached_input_price IS NOT NULL
            AND sale_output_price IS NOT NULL
        )
    );

COMMENT ON COLUMN model_price_service_tiers.sale_uncached_input_price IS
    'Fast 档对外绝对售价；整组为空时回退「该档基准价 × 全局售价倍率」。';

-- 全局售价倍率：目标是改造前后计费结果不变，所以按「最可信的现行倍率」三级取值：
--   1. 现存 enabled 线路的 price_ratio —— 正在生效的配置；
--   2. 最近一条计费快照的 price_ratio —— 线路已被清理但账务留下了实际用过的倍率，
--      这比默认值更贴近真相（漏掉这一级会让售价按 1.0 结算，直接放大数倍）；
--   3. 1.0 —— 全新库，等于直接卖基准价，不会亏本。
INSERT INTO app_settings (key, value, description)
SELECT
    'gateway.model_sale_price_ratio',
    jsonb_build_object('ratio', COALESCE(
        (SELECT rt.price_ratio FROM routes rt WHERE rt.status = 'enabled' ORDER BY rt.id LIMIT 1),
        (SELECT ps.price_ratio FROM price_snapshots ps
          WHERE ps.price_ratio IS NOT NULL AND ps.price_ratio > 0
          ORDER BY ps.id DESC LIMIT 1),
        1.0
    )::text),
    '模型未配置绝对售价时，客户售价 = 模型基准价 × 本倍率。'
ON CONFLICT (key) DO NOTHING;

-- 模型停用原因：原先挂在 route_model_offerings 上，降维后由模型继承。
-- 管理员需要能区分「我手动下架的」和「渠道解绑连带停的」，否则一批模型集体变灰无从解释。
ALTER TABLE models
    ADD COLUMN disabled_reason TEXT,
    ADD COLUMN disabled_at TIMESTAMPTZ;

ALTER TABLE models
    ADD CONSTRAINT ck_models_disabled_reason CHECK (
        disabled_reason IS NULL
        OR disabled_reason IN ('manual_delisted', 'binding_disabled', 'channel_disabled')
    );

COMMENT ON COLUMN models.disabled_reason IS
    '停用直接原因：manual_delisted 管理员主动下架；binding_disabled 最后一条渠道绑定被停用或解除；'
    'channel_disabled 最后一条可用渠道被停用。enabled 时为空。';

-- ── 4. 模型目录元数据：系列与厂商 ────────────────────────────────────────────
ALTER TABLE models ADD COLUMN family TEXT NOT NULL DEFAULT '';
COMMENT ON COLUMN models.family IS
    '模型系列（来自 models.dev feed 的 family），仅用于列表分组展示，空串表示未归类。';

CREATE TABLE model_labs (
    id bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
    slug TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    -- 存 SVG 内容而非 URL：图标属于展示不能挂的部分，且避免用户浏览器直连第三方。
    logo_svg TEXT NOT NULL DEFAULT '',
    logo_synced_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE model_labs IS
    '模型出品方（models.dev 的 lab），与 models.owned_by 按 slug 关联，不设外键：'
    'owned_by 是 OpenAI 兼容契约字段，必须保持字符串形态。';

-- 用现有模型的 owned_by 播种，logo 由同步任务补齐。
INSERT INTO model_labs (slug, name)
SELECT DISTINCT m.owned_by, m.owned_by
FROM models m
WHERE m.owned_by <> ''
ON CONFLICT (slug) DO NOTHING;

-- ── 5. 限流默认值设置正名 ────────────────────────────────────────────────────
-- gateway.route_rate_limit_defaults 的语义一直是「全局默认限流」，名字里的 route 是历史包袱。
-- 线路概念消失后这个名字只会误导人以为还有线路维度，所以一并改名。
UPDATE app_settings
SET key = 'gateway.request_rate_limit_defaults',
    description = '入口请求限流默认值（rpm/rpd）；用户未单独配置时生效。'
WHERE key = 'gateway.route_rate_limit_defaults';

-- 运行时控制白名单跟着改：该约束限定哪些 app_setting 可以下发到 Redis 生效。
ALTER TABLE runtime_control_operations
    DROP CONSTRAINT ck_runtime_control_operations_target;

-- 历史操作记录也要跟着改名，否则新约束会被存量行违反。
UPDATE runtime_control_operations
SET setting_key = 'gateway.request_rate_limit_defaults'
WHERE setting_key = 'gateway.route_rate_limit_defaults';

ALTER TABLE runtime_control_operations
    ADD CONSTRAINT ck_runtime_control_operations_target CHECK (
        ((kind = 'channel_capacity'::text) AND (channel_id IS NOT NULL) AND (setting_key IS NULL))
        OR ((kind = 'app_setting'::text) AND (channel_id IS NULL) AND (setting_key = ANY (ARRAY[
            'gateway.request_rate_limit_defaults'::text,
            'gateway.concurrency_defaults'::text,
            'gateway.circuit_breaker'::text,
            'gateway.routing_balance'::text
        ])))
        OR ((kind = 'runtime_state_epoch'::text) AND (channel_id IS NULL)
            AND (setting_key = 'gateway.runtime_state_epoch'::text))
    );

-- ── 6. 删除 route 与用户模型策略 ─────────────────────────────────────────────
-- 供给资格改由「模型 enabled + 至少一条可用渠道」这一不变量保证，
-- 不再需要 route_model_offerings 表达售卖意图，也不再需要用户级 allow/deny。
ALTER TABLE api_keys DROP COLUMN route_id;
ALTER TABLE request_records DROP COLUMN route_id;
ALTER TABLE routing_decision_traces DROP COLUMN route_id;

DROP TABLE route_model_offerings;
DROP TABLE route_channels;
DROP TABLE routes;
DROP TABLE user_model_policies;
