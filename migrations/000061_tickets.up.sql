-- 用户反馈工单：Console 用户提交与回复，Admin 运营处理，worker 负责超期自动关闭与孤儿附件清理。
--
-- [归属层级]
-- 沿用 user → api_key 的两级模型：工单直接挂 users.id，无团队/项目层。对外一律用 uid（uuid）定位，
-- 不暴露自增主键。
--
-- [状态机]
-- open（待客服处理）/ pending（客服已回复，待用户）/ resolved（客服标记已解决，用户回复即重开）/
-- closed（终态，不可回复）。用户回复：pending|resolved → open；客服回复：open → pending；
-- resolved 超期未回复由 worker 自动关闭。
--
-- [正文与附件]
-- 消息正文是服务端白名单校验后的 Tiptap JSON（body），body_text 是提取的纯文本（列表预览用）。
-- 附件先上传后绑定：message_id 为 NULL 表示编辑中的"孤儿"附件，提交消息时绑定；
-- 超期孤儿由 worker 清理。初版二进制直接存 bytea（工单截图量小），换对象存储时只动存储层。

CREATE SEQUENCE public.tickets_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.tickets (
    -- id: 内部主键，单调递增。--
    id bigint NOT NULL,
    -- uid: 对外标识，Console/Admin API 路径与前端路由用它定位工单。--
    uid uuid NOT NULL,
    -- user_id: 工单归属用户；用户删除时级联删除其工单。--
    user_id bigint NOT NULL,
    -- subject: 工单主题，列表页直接展示。--
    subject text NOT NULL,
    -- category: 问题分类。billing=账务 / api=API 使用 / model=模型 / account=账号 / other=其他。--
    category text NOT NULL,
    -- status: 状态机当前态，见文件头注释。--
    status text DEFAULT 'open'::text NOT NULL,
    -- user_unread: 客服回复后置 true，用户打开详情时清除；Console 列表红点与侧栏角标用。--
    user_unread boolean DEFAULT false NOT NULL,
    -- admin_unread: 用户创建/回复后置 true，运营打开详情时清除；Admin 队列红点用。--
    admin_unread boolean DEFAULT true NOT NULL,
    -- last_message_at: 最后一条消息时间，列表排序键；也是 resolved 自动关闭的超期基准。--
    last_message_at timestamp with time zone DEFAULT now() NOT NULL,
    -- resolved_at: 进入 resolved 的时间；重开时清空。--
    resolved_at timestamp with time zone,
    -- closed_at: 进入 closed 的时间。--
    closed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT tickets_subject_check CHECK (((btrim(subject) <> ''::text) AND (char_length(subject) <= 200))),
    CONSTRAINT tickets_category_check CHECK ((category = ANY (ARRAY['billing'::text, 'api'::text, 'model'::text, 'account'::text, 'other'::text]))),
    CONSTRAINT tickets_status_check CHECK ((status = ANY (ARRAY['open'::text, 'pending'::text, 'resolved'::text, 'closed'::text])))
);

ALTER SEQUENCE public.tickets_id_seq OWNED BY public.tickets.id;

ALTER TABLE ONLY public.tickets
    ALTER COLUMN id SET DEFAULT nextval('public.tickets_id_seq'::regclass);

ALTER TABLE ONLY public.tickets
    ADD CONSTRAINT tickets_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.tickets
    ADD CONSTRAINT tickets_uid_key UNIQUE (uid);

ALTER TABLE ONLY public.tickets
    ADD CONSTRAINT tickets_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

-- Console「我的工单」列表：按用户取最近更新。
CREATE INDEX idx_tickets_user_recent ON public.tickets USING btree (user_id, last_message_at DESC);

-- Admin 队列：按状态过滤后取最近更新；worker 自动关闭也走 status 前缀。
CREATE INDEX idx_tickets_status_recent ON public.tickets USING btree (status, last_message_at DESC);

CREATE SEQUENCE public.ticket_messages_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.ticket_messages (
    -- id: 主键，同时是工单内消息的时间序（对话流按 id 升序展示）。--
    id bigint NOT NULL,
    -- ticket_id: 所属工单；工单删除时级联删除消息。--
    ticket_id bigint NOT NULL,
    -- author_type: 发言方。user=工单归属用户 / admin=运营（单管理员，无需记具体身份）。--
    author_type text NOT NULL,
    -- body: 白名单校验后的 Tiptap JSON 文档；图片节点 src 是 attachment:{uid} 稳定引用。--
    body jsonb NOT NULL,
    -- body_text: 从 body 提取的纯文本，列表预览与后续搜索用；上限在服务层约束。--
    body_text text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ticket_messages_author_type_check CHECK ((author_type = ANY (ARRAY['user'::text, 'admin'::text])))
);

ALTER SEQUENCE public.ticket_messages_id_seq OWNED BY public.ticket_messages.id;

ALTER TABLE ONLY public.ticket_messages
    ALTER COLUMN id SET DEFAULT nextval('public.ticket_messages_id_seq'::regclass);

ALTER TABLE ONLY public.ticket_messages
    ADD CONSTRAINT ticket_messages_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.ticket_messages
    ADD CONSTRAINT ticket_messages_ticket_id_fkey FOREIGN KEY (ticket_id) REFERENCES public.tickets(id) ON DELETE CASCADE;

-- 详情页对话流：按工单取全部消息（升序）。
CREATE INDEX idx_ticket_messages_ticket ON public.ticket_messages USING btree (ticket_id, id);

CREATE SEQUENCE public.ticket_attachments_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.ticket_attachments (
    -- id: 内部主键。--
    id bigint NOT NULL,
    -- uid: 对外标识，签名下载 URL 与 Tiptap 图片节点的 attachment:{uid} 引用都用它。--
    uid uuid NOT NULL,
    -- ticket_id: 绑定后的所属工单；上传时为 NULL（新建工单时消息与工单都尚不存在）。--
    ticket_id bigint,
    -- message_id: 绑定后的所属消息；NULL = 孤儿（编辑中未提交），超期由 worker 清理。--
    message_id bigint,
    -- user_id: 上传者（用户侧）；admin 上传时为 NULL。孤儿数量按此列限流。--
    user_id bigint,
    -- uploader_type: 上传方，与 user_id 的空否严格对应（见 CHECK）。--
    uploader_type text NOT NULL,
    -- file_name: 原始文件名，仅展示用。--
    file_name text NOT NULL,
    -- mime_type: 仅允许常见图片类型；服务层同时做内容嗅探双重校验。--
    mime_type text NOT NULL,
    -- size_bytes: 字节数，与服务层 5MB 上限一致，双保险。--
    size_bytes integer NOT NULL,
    -- data: 附件二进制。初版直接落库（TOAST 承载），换对象存储时改为外部键。--
    data bytea NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ticket_attachments_uploader_type_check CHECK ((uploader_type = ANY (ARRAY['user'::text, 'admin'::text]))),
    CONSTRAINT ticket_attachments_uploader_user_check CHECK (((uploader_type = 'user'::text) = (user_id IS NOT NULL))),
    CONSTRAINT ticket_attachments_file_name_check CHECK (((btrim(file_name) <> ''::text) AND (char_length(file_name) <= 200))),
    CONSTRAINT ticket_attachments_mime_type_check CHECK ((mime_type = ANY (ARRAY['image/png'::text, 'image/jpeg'::text, 'image/webp'::text, 'image/gif'::text]))),
    CONSTRAINT ticket_attachments_size_bytes_check CHECK (((size_bytes > 0) AND (size_bytes <= 5242880)))
);

ALTER SEQUENCE public.ticket_attachments_id_seq OWNED BY public.ticket_attachments.id;

ALTER TABLE ONLY public.ticket_attachments
    ALTER COLUMN id SET DEFAULT nextval('public.ticket_attachments_id_seq'::regclass);

ALTER TABLE ONLY public.ticket_attachments
    ADD CONSTRAINT ticket_attachments_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.ticket_attachments
    ADD CONSTRAINT ticket_attachments_uid_key UNIQUE (uid);

ALTER TABLE ONLY public.ticket_attachments
    ADD CONSTRAINT ticket_attachments_ticket_id_fkey FOREIGN KEY (ticket_id) REFERENCES public.tickets(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.ticket_attachments
    ADD CONSTRAINT ticket_attachments_message_id_fkey FOREIGN KEY (message_id) REFERENCES public.ticket_messages(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.ticket_attachments
    ADD CONSTRAINT ticket_attachments_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

-- 详情页附件元数据：按工单取已绑定附件。
CREATE INDEX idx_ticket_attachments_ticket ON public.ticket_attachments USING btree (ticket_id) WHERE (ticket_id IS NOT NULL);

-- 孤儿附件：用户孤儿配额检查（user_id 前缀）与 worker 超期清理（created_at）都只扫这个小索引。
CREATE INDEX idx_ticket_attachments_orphan ON public.ticket_attachments USING btree (user_id, created_at) WHERE (message_id IS NULL);
