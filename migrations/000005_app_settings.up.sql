-- app_settings 是全局可编辑系统设置的通用 key→JSONB 存储(管理端可改、后端运行时读)。
-- 与 env 启动配置互补:env 管进程级不可热改的阈值;本表管运营在后台可动态调整的策略。
-- 首个使用者是 Anthropic beta 转发策略(key='anthropic.beta_policy'),将来 OpenAI/Gemini
-- 各自的 provider 配置直接新增 key(如 'openai.xxx'),无需改表结构。
CREATE TABLE public.app_settings (
    -- key: 设置项唯一标识(建议 '<provider>.<name>' 命名,如 'anthropic.beta_policy')。--
    key text NOT NULL,
    -- value: 设置项内容(结构由使用方定义并校验;DB 只保证是合法 JSON)。--
    value jsonb NOT NULL,
    -- updated_at: 最近一次写入时间。--
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    CONSTRAINT app_settings_key_check CHECK ((key <> ''::text))
);

ALTER TABLE ONLY public.app_settings
    ADD CONSTRAINT app_settings_pkey PRIMARY KEY (key);

-- ---------------------------------------------------------------------------
-- 后续迁移补充的设计说明（列/约束演进，原 ALTER 迁移的中文注释归档）：
-- ---------------------------------------------------------------------------
-- [000069_add_app_settings_description]
-- 为 app_settings 增加 description 列:让每行配置在库里自解释(不必回查代码就知道这个 key 是什么意思)。
-- 权威说明来自代码里的配置注册表(settings registry),写入时一并落库;本列是注册表说明的持久化快照。
