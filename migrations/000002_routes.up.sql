-- Route 是面向客户的「线路 / 渠道商品」（阶段 15）。
-- 线路决定「显式渠道池 + 调度策略」，叠加在既有能力闸门 / 熔断 / 协议过滤之上，不改变它们。
CREATE SEQUENCE public.routes_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.routes (
    -- id: 主键。--
    id bigint NOT NULL,
    -- name: 对外商品名（经济 / 稳定 / C-专线 ...），全局唯一。--
    name text NOT NULL,
    -- mode: 选路策略。balanced=显式池内负载均衡；fixed=锁定单条渠道。--
    mode text NOT NULL,
    -- status: 线路启停状态。--
    status text NOT NULL,
    -- description: 线路简介（展示给客户的商品说明）。--
    description text,
    -- created_at: 记录创建时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    -- updated_at: 记录更新时间。--
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    price_ratio numeric(20,10) DEFAULT 1.0 NOT NULL,
    rpm_limit integer,
    tpm_limit integer,
    rpd_limit integer,
    archived_at timestamp with time zone,
    -- sticky_enabled: 会话粘性路由开关（大 uncache 缺口 P0）。NULL=继承系统设置
    -- gateway.routing_sticky.enabled_default；true/false=线路显式覆盖。--
    sticky_enabled boolean,
    CONSTRAINT ck_routes_archived_at CHECK (((status = 'archived'::text) = (archived_at IS NOT NULL))),
    CONSTRAINT routes_mode_check CHECK ((mode = ANY (ARRAY['balanced'::text, 'fixed'::text]))),
    CONSTRAINT routes_name_check CHECK ((name <> ''::text)),
    CONSTRAINT routes_price_ratio_check CHECK ((price_ratio >= (0)::numeric)),
    CONSTRAINT routes_rpd_limit_check CHECK (((rpd_limit IS NULL) OR (rpd_limit >= 0))),
    CONSTRAINT routes_rpm_limit_check CHECK (((rpm_limit IS NULL) OR (rpm_limit >= 0))),
    CONSTRAINT routes_status_check CHECK ((status = ANY (ARRAY['enabled'::text, 'disabled'::text, 'archived'::text]))),
    CONSTRAINT routes_tpm_limit_check CHECK (((tpm_limit IS NULL) OR (tpm_limit >= 0)))
);

ALTER SEQUENCE public.routes_id_seq OWNED BY public.routes.id;

ALTER TABLE ONLY public.routes ALTER COLUMN id SET DEFAULT nextval('public.routes_id_seq'::regclass);

ALTER TABLE ONLY public.routes
    ADD CONSTRAINT routes_name_key UNIQUE (name);

ALTER TABLE ONLY public.routes
    ADD CONSTRAINT routes_pkey PRIMARY KEY (id);

-- ---------------------------------------------------------------------------
-- 后续迁移补充的设计说明（列/约束演进，原 ALTER 迁移的中文注释归档）：
-- ---------------------------------------------------------------------------
-- [000055_add_routes_price_ratio]
-- 为 route（线路 = 分组 / 档）增加价格倍率（DEC-026 倍率定价）。
-- 客户最终售价 = model_prices（模型基准售价） × routes.price_ratio。默认 1.0（与基准价同价）。
-- 同一线路对所有模型套用同一倍率（new-api groupRatio 口径）；如需逐模型差异，后续另建表，本期不做。
-- [000057_remove_builtin_routes]
-- 移除内置线路（经济/稳定）及 is_builtin 标识；线路须显式绑定到 API Key 或项目默认。
-- [000059_add_routes_rate_limits]
-- 为线路增加线路级限流上限（DEC-027）：RPM 每分钟请求 / TPM 每分钟 token / RPD 每日请求。
-- 三列均可空：NULL 表示「继承全局默认」，0 表示「显式不限」，>0 表示具体上限。
-- 计数在 Redis 滑动窗口按 (线路, 用户) 复合主体执行（同一用户在该线路下的所有 Key 共享一个桶，
-- 多建 Key 无法放大配额）；本列只持久化线路的「上限模板」。
-- [000066_add_archived_status]
-- 实体归档生命周期：providers / channels / routes 三表 status 增第三态 archived，
-- 并加 archived_at 时间列 + 一致性不变量（archived_at 有值 ⟺ status='archived'）。
-- 归档 = 只改状态、不删数据、完全可逆；路由候选已按 status='enabled' 过滤，archived 天然被排除。
-- [sticky_enabled 追加（大 uncache 缺口 P0）]
-- 会话粘性路由的线路级开关：同会话请求钉住上次成功渠道以保上游 prompt cache。
-- NULL=继承系统设置 gateway.routing_sticky.enabled_default；true/false=显式覆盖。
-- 绑定关系存 Redis（sticky:{protocol}:{route_id}:{api_key_id}:{session_key_hash}），本列只持久化开关。
--
-- providers
