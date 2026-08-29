-- Admin 站内消息中心（告警通道 MVP，决策见 docs/changes/2026-08-29-multi-currency/PLAN.md §12.C.3）：
-- worker / 服务端产生的运营告警与系统通知统一落表，admin 顶栏铃铛轮询未读数，消息页分页查看并标记已读。
-- 只追加 + 标记已读，不提供删除；后续用户工单等站内通知复用本表范式（扩 topic 即可，不另建表）。
CREATE SEQUENCE public.admin_messages_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.admin_messages (
    -- id: 主键，单调递增，同时充当列表排序键（新消息在前）。--
    id bigint NOT NULL,
    -- severity: 告警级别。info=一般通知 / warning=需关注 / critical=需立即处理。--
    severity text NOT NULL,
    -- topic: 消息来源域（如 fx_rate / margin / reconciliation / system），自由文本便于扩展，供筛选。--
    topic text NOT NULL,
    -- title: 一句话标题，列表页直接展示。--
    title text NOT NULL,
    -- body: 详情正文（纯文本），应包含定位问题所需的关键字段，让告警不用再查库。--
    body text NOT NULL,
    -- source: 产生者标识（worker 名 / 服务名），排查消息来源用。--
    source text NOT NULL,
    -- dedupe_key: 防重键。同 key 已存在未读消息时新写入静默跳过（部分唯一索引 + ON CONFLICT DO NOTHING），
    -- 防止周期任务把同一告警重复轰炸成一屏；标记已读后同 key 可再次产生新消息。NULL = 不去重。--
    dedupe_key text,
    -- created_at: 消息产生时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    -- read_at: 标记已读时间；NULL = 未读。--
    read_at timestamp with time zone,
    CONSTRAINT admin_messages_severity_check CHECK ((severity = ANY (ARRAY['info'::text, 'warning'::text, 'critical'::text]))),
    CONSTRAINT admin_messages_topic_check CHECK ((btrim(topic) <> ''::text)),
    CONSTRAINT admin_messages_title_check CHECK ((btrim(title) <> ''::text)),
    CONSTRAINT admin_messages_source_check CHECK ((btrim(source) <> ''::text))
);

ALTER SEQUENCE public.admin_messages_id_seq OWNED BY public.admin_messages.id;

ALTER TABLE ONLY public.admin_messages
    ALTER COLUMN id SET DEFAULT nextval('public.admin_messages_id_seq'::regclass);

ALTER TABLE ONLY public.admin_messages
    ADD CONSTRAINT admin_messages_pkey PRIMARY KEY (id);

-- 未读去重：仅约束「未读」窗口内的同 key 消息，读掉即解除，供 ON CONFLICT (dedupe_key) WHERE read_at IS NULL 仲裁。
-- 谓词刻意不加 dedupe_key IS NOT NULL：唯一索引中 NULL 互不冲突（NULLS DISTINCT），无 key 消息天然不受约束；
-- 谓词必须与 INSERT 的 ON CONFLICT 推断条件完全一致，多一个条件都会让推断失败（42P10）。
CREATE UNIQUE INDEX uq_admin_messages_unread_dedupe
    ON public.admin_messages USING btree (dedupe_key)
    WHERE (read_at IS NULL);

-- 未读数轮询（顶栏铃铛高频访问）与未读列表都只扫这个小索引。
CREATE INDEX idx_admin_messages_unread ON public.admin_messages USING btree (id DESC) WHERE (read_at IS NULL);
