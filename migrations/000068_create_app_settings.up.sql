-- app_settings 是全局可编辑系统设置的通用 key→JSONB 存储(管理端可改、后端运行时读)。
-- 与 env 启动配置互补:env 管进程级不可热改的阈值;本表管运营在后台可动态调整的策略。
-- 首个使用者是 Anthropic beta 转发策略(key='anthropic.beta_policy'),将来 OpenAI/Gemini
-- 各自的 provider 配置直接新增 key(如 'openai.xxx'),无需改表结构。
CREATE TABLE app_settings (
    -- key: 设置项唯一标识(建议 '<provider>.<name>' 命名,如 'anthropic.beta_policy')。--
    key TEXT PRIMARY KEY CHECK (key <> ''),

    -- value: 设置项内容(结构由使用方定义并校验;DB 只保证是合法 JSON)。--
    value JSONB NOT NULL,

    -- updated_at: 最近一次写入时间。--
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
