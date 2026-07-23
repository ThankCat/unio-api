-- Usage line item 是一次 usage record 下受控的附加计量事实。
CREATE SEQUENCE public.usage_line_items_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.usage_line_items (
    -- id: 主键。--
    id bigint NOT NULL,
    -- usage_record_id: 所属 usage record ID。--
    usage_record_id bigint NOT NULL,
    -- kind: 已登记的附加计量项类型。--
    kind text NOT NULL,
    quantity bigint NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT usage_line_items_kind_check CHECK ((kind = ANY (ARRAY['server_web_search_request'::text, 'server_web_fetch_request'::text]))),
    CONSTRAINT usage_line_items_quantity_check CHECK ((quantity > 0))
);

ALTER SEQUENCE public.usage_line_items_id_seq OWNED BY public.usage_line_items.id;

ALTER TABLE ONLY public.usage_line_items ALTER COLUMN id SET DEFAULT nextval('public.usage_line_items_id_seq'::regclass);

ALTER TABLE ONLY public.usage_line_items
    ADD CONSTRAINT uq_usage_line_items_record_kind UNIQUE (usage_record_id, kind);

ALTER TABLE ONLY public.usage_line_items
    ADD CONSTRAINT usage_line_items_pkey PRIMARY KEY (id);

CREATE INDEX idx_usage_line_items_usage_record_id ON public.usage_line_items USING btree (usage_record_id, id);

ALTER TABLE ONLY public.usage_line_items
    ADD CONSTRAINT usage_line_items_usage_record_id_fkey FOREIGN KEY (usage_record_id) REFERENCES public.usage_records(id);
