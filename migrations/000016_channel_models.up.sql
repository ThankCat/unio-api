-- Channel model 表示某条 channel 能服务哪个模型及对应上游模型名。
CREATE SEQUENCE public.channel_models_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.channel_models (
    -- id: 主键。--
    id bigint NOT NULL,
    -- channel_id: 可服务该模型的 channel ID。--
    channel_id bigint NOT NULL,
    -- model_id: Unio 侧模型 ID。--
    model_id bigint NOT NULL,
    -- upstream_model: 转发到上游时使用的模型名。--
    upstream_model text NOT NULL,
    -- status: channel-model 映射启停状态。--
    status text NOT NULL,
    -- created_at: 记录创建时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    -- updated_at: 记录更新时间。--
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT channel_models_status_check CHECK ((status = ANY (ARRAY['enabled'::text, 'disabled'::text])))
);

ALTER SEQUENCE public.channel_models_id_seq OWNED BY public.channel_models.id;

ALTER TABLE ONLY public.channel_models ALTER COLUMN id SET DEFAULT nextval('public.channel_models_id_seq'::regclass);

ALTER TABLE ONLY public.channel_models
    ADD CONSTRAINT channel_models_channel_id_model_id_key UNIQUE (channel_id, model_id);

ALTER TABLE ONLY public.channel_models
    ADD CONSTRAINT channel_models_pkey PRIMARY KEY (id);

CREATE INDEX idx_channel_models_channel_id ON public.channel_models USING btree (channel_id);

CREATE INDEX idx_channel_models_model_id ON public.channel_models USING btree (model_id);

ALTER TABLE ONLY public.channel_models
    ADD CONSTRAINT channel_models_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id);

ALTER TABLE ONLY public.channel_models
    ADD CONSTRAINT channel_models_model_id_fkey FOREIGN KEY (model_id) REFERENCES public.models(id);
