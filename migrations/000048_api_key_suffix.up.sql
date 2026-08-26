-- 记录 API Key 明文的末 4 位，用于「sk_unio_2opsq71z⋯⋯9f3a」这种首尾可辨的掩码展示。
--
-- 只靠前缀识别有个实际问题：前 8 位随机在列表里往往长得很像，用户手里拿着一串明文
-- 想对上是哪一把时，尾部几位反而是更常被记住的那一段（SDK 报错、日志截断通常留尾）。
-- Stripe 与 GitHub 都是同样做法。
--
-- 安全边界：明文共 56 字符（sk_unio_ + 48 位 base36），暴露 8 位前缀加 4 位后缀之后
-- 仍余 44 位随机、约 227 bit 熵，暴力枚举不可行；认证依旧只比对 key_hash。
--
-- 存量行留 NULL：明文早已丢弃，无从回填。展示层对 NULL 退化成「只有前缀」的掩码。
ALTER TABLE public.api_keys
    ADD COLUMN key_suffix text;

COMMENT ON COLUMN public.api_keys.key_suffix IS 'API Key 明文末 4 位，仅供掩码展示时拼出尾段。NULL 表示该 key 建于本列之前。认证不使用此列。';
