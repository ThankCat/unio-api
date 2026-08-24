-- 为 request_records 增加客户端会话审计三列：把 Codex CLI 的多轮上下文标识落库，
-- 使一次会话内的多个请求可在 Console/Admin 侧按线程与轮次串联排查。
--
-- 来源是 Codex v0.147 的请求头 x-codex-turn-metadata 与 body 内 client_metadata，
-- 二者携带同一组标识。这些值仅用于本地审计，不转发上游。
-- 其它协议（Anthropic Messages / OpenAI Chat）无对应语义时保持 NULL。
ALTER TABLE public.request_records
    -- client_thread_id: 客户端会话线程标识（Codex thread_id），跨轮稳定。--
    ADD COLUMN IF NOT EXISTS client_thread_id text,
    -- client_turn_id: 单轮标识（Codex turn_id），一轮内多次上游尝试共享。--
    ADD COLUMN IF NOT EXISTS client_turn_id text,
    -- client_request_kind: 客户端声明的请求种类（Codex request_kind，如 turn/compact）。--
    ADD COLUMN IF NOT EXISTS client_request_kind text;

-- 按线程回溯整段会话是主要查询形态；仅对非空值建索引，避免为其它协议的 NULL 行付出代价。
CREATE INDEX IF NOT EXISTS idx_request_records_client_thread_created_at
    ON public.request_records USING btree (client_thread_id, created_at DESC)
    WHERE (client_thread_id IS NOT NULL);
