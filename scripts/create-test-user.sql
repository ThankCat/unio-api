-- 创建一个开发用测试用户（幂等）。
--
-- 客户侧不走密码登录（认证靠 API Key），password_hash 仅占位、不参与登录校验。
-- 已存在同 email（大小写不敏感）时不重复插入，也不改动已有数据。
--
-- 执行：psql "$DATABASE_URL" -f scripts/create-test-user.sql
-- 建好用户后，其余数据（渠道/模型/线路/价格/API Key 等）在后台自行创建。

INSERT INTO users (email, password_hash, display_name)
SELECT 'dev@unio.local', 'seed-placeholder-not-a-real-hash', 'Dev User'
WHERE NOT EXISTS (
    SELECT 1 FROM users WHERE lower(email) = lower('dev@unio.local')
);
