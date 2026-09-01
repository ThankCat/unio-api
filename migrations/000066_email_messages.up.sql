-- 邮件发送记录（docs/changes/2026-09-01-email-verification-code）。
--
-- 验证码批次的同步发送路径在每次 SMTP 提交完成后（无论成败）写入一行事实记录，
-- 供 Admin 客户中心「邮件」列表排查与审计。这是发送事实日志，不是投递队列：
-- status=sent 只代表 SMTP 服务商接受本次提交，不代表邮件进入收件箱。

CREATE SEQUENCE public.email_messages_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.email_messages (
    -- id: 主键。--
    id bigint NOT NULL,
    -- email_type: 邮件类型：verification_register / verification_login / verification_password_reset / verification_password_set / verification_password_change / test。--
    email_type text NOT NULL,
    -- recipient: 收件人地址。--
    recipient text NOT NULL,
    -- sender: 本封实际使用的发件地址（为未来多发件箱预留，逐封快照）。--
    sender text NOT NULL,
    -- subject: 邮件主题。--
    subject text NOT NULL,
    -- body_html: 完整 HTML 正文（验证码邮件含验证码；保留期限决策前不自动清理）。--
    body_html text NOT NULL,
    -- status: 发送结果：sent = SMTP 接受提交；failed = 提交失败。--
    status text NOT NULL,
    -- error_summary: 失败时的安全错误摘要（不含凭据），成功为 NULL。--
    error_summary text,
    -- locale: 模板语言：zh / en。--
    locale text NOT NULL,
    -- duration_ms: SMTP 提交耗时（毫秒）。--
    duration_ms integer,
    -- sent_at: SMTP 接受提交的时间；failed 为 NULL。--
    sent_at timestamp with time zone,
    -- created_at: 记录写入时间（列表默认排序键）。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT email_messages_status_check CHECK ((status = ANY (ARRAY['sent'::text, 'failed'::text]))),
    CONSTRAINT email_messages_state_check CHECK (((status = 'sent'::text AND sent_at IS NOT NULL) OR (status = 'failed'::text AND sent_at IS NULL))),
    CONSTRAINT email_messages_email_type_check CHECK ((btrim(email_type) <> ''::text)),
    CONSTRAINT email_messages_recipient_check CHECK ((btrim(recipient) <> ''::text))
);

ALTER SEQUENCE public.email_messages_id_seq OWNED BY public.email_messages.id;

ALTER TABLE ONLY public.email_messages
    ALTER COLUMN id SET DEFAULT nextval('public.email_messages_id_seq'::regclass);

ALTER TABLE ONLY public.email_messages
    ADD CONSTRAINT email_messages_pkey PRIMARY KEY (id);

-- 列表默认按时间倒序；筛选维度：收件人 / 类型 / 结果。
CREATE INDEX idx_email_messages_created_at ON public.email_messages USING btree (created_at DESC);
CREATE INDEX idx_email_messages_recipient ON public.email_messages USING btree (recipient);
CREATE INDEX idx_email_messages_email_type ON public.email_messages USING btree (email_type);
CREATE INDEX idx_email_messages_status ON public.email_messages USING btree (status);
