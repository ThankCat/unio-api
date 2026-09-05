-- 订阅账号指纹收敛开关（2026-09-05 Codex 身份隔离改造 WP4）。
--
-- fingerprint_mode：
--   off    （默认）客户端设备 id 按「客户 × 账号」1:1 映射出站，上游看到的设备数与真实客户数一致；
--   device  账号内全部客户共用一个由账号种子派生的 installation_id（上游看到 1 台设备 + 各自独立会话）。
-- Sub2API 同类开关默认关闭：有用户在开启收敛后出现过额度缩水，Unio 同样默认 off、由运维按实测选择。
-- 会话/对话 id 不在收敛范围内（与按对话的 prompt cache 亲和冲突）。
--
-- fingerprint_seed：系统管理的账号级随机种子，首次切到非 off 时生成、永不改写、切回 off 也保留
-- （再切回来时设备身份不变）；永不在 Admin API 暴露。永不用 DB 主键充当上游身份。
ALTER TABLE public.subscription_accounts
    ADD COLUMN fingerprint_mode text NOT NULL DEFAULT 'off',
    ADD COLUMN fingerprint_seed uuid;

ALTER TABLE public.subscription_accounts
    ADD CONSTRAINT subscription_accounts_fingerprint_mode_check
        CHECK (fingerprint_mode IN ('off', 'device'));

COMMENT ON COLUMN public.subscription_accounts.fingerprint_mode IS
    '指纹收敛档位：off=客户×账号 1:1 映射（默认）；device=账号内收敛 installation_id。会话 id 永不收敛。';
COMMENT ON COLUMN public.subscription_accounts.fingerprint_seed IS
    '系统管理的账号级随机种子（device 派生 installation_id 用）；首次开启收敛时生成、永不改写、不对外暴露。';
