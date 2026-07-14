-- 折叠 user → project → api_key 三级为 user → api_key 两级，彻底移除 projects 概念。
-- API Key、模型策略与请求归属全部直接挂在用户上。
-- 同时把线路改为 API Key 必填：彻底移除「用户/项目默认线路」回落，线路只认 api_keys.route_id。
-- 数据无需保留，但仍写正确回填，保证存量库平滑迁移。
-- 1. api_keys.project_id → api_keys.user_id（API Key 直接归属用户）。
-- 回填：经由旧 projects 把 API Key 归属解析到用户。
-- 认证和管理接口常按 user_id 查询 API Key。
-- DROP COLUMN 自动连带删除 project_id 的外键与 idx_api_keys_project_id 索引。
-- 2. api_keys.route_id 改为必填：线路必须显式绑定到 Key（移除默认线路回落）。
-- 兜底清理历史无线路的 Key（重置流程下无此类行）；随后置 NOT NULL，DB 层兜底「线路必填」。
-- 3. project_model_policies → user_model_policies（模型可见性策略改挂用户）。
CREATE TABLE public.user_model_policies (
    -- user_id: 策略所属用户 ID。--
    user_id bigint NOT NULL,
    -- model_id: 策略作用的模型 ID。--
    model_id bigint NOT NULL,
    -- visibility: 模型对该用户的可见性。--
    visibility text NOT NULL,
    -- created_at: 记录创建时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    -- updated_at: 记录更新时间。--
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT user_model_policies_visibility_check CHECK ((visibility = ANY (ARRAY['allowed'::text, 'denied'::text])))
);

ALTER TABLE ONLY public.user_model_policies
    ADD CONSTRAINT user_model_policies_pkey PRIMARY KEY (user_id, model_id);

CREATE INDEX idx_user_model_policies_model_id ON public.user_model_policies USING btree (model_id);

CREATE INDEX idx_user_model_policies_user_visibility ON public.user_model_policies USING btree (user_id, visibility);

ALTER TABLE ONLY public.user_model_policies
    ADD CONSTRAINT user_model_policies_model_id_fkey FOREIGN KEY (model_id) REFERENCES public.models(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.user_model_policies
    ADD CONSTRAINT user_model_policies_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;
