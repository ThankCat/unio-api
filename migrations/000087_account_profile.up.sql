-- 号池账号的上游画像快照（2026-09-06）。
--
-- ChatGPT 后端有两个只读接口能给出账号本身的状态：accounts/check（套餐、订阅 entitlement、到期/续订/取消、
-- 欠费、停用、账号结构）与 me（用户画像：MFA、注册时间、国家/地区、组织与角色）。本列存「刷新状态」
-- 拿到的脱敏快照，供管理端各列详情展示；订阅到期与套餐同时回写到既有列（plan_type / subscription_expires_at），
-- 让原本手工维护的到期日改为上游权威值。
ALTER TABLE public.subscription_accounts
    ADD COLUMN account_profile jsonb;

COMMENT ON COLUMN public.subscription_accounts.account_profile IS
    '上游账号画像快照（accounts/check + me + wham/usage 的 credits）：套餐/订阅/状态/用户/组织/地区，含 fetched_at 与分项错误。展示缓存。';
