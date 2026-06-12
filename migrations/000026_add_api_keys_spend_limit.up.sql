-- API Key 费用上限：生命周期累计封顶（M7）。
-- 口径同 OpenRouter：每把 Key 设一个累计花费上限，spent_total 达到 spend_limit 即停用该 Key。
-- 假设单币种部署（与计费币种一致，实践为 USD）；spent_total 在 settlement capture 时累加客户实扣金额。
ALTER TABLE api_keys
    -- spend_limit: 该 Key 生命周期累计花费上限；NULL 表示不限额。--
    ADD COLUMN spend_limit NUMERIC(20, 10) CHECK (spend_limit IS NULL OR spend_limit >= 0),
    -- spent_total: 该 Key 迄今累计被扣金额（settlement capture 时累加）。--
    ADD COLUMN spent_total NUMERIC(20, 10) NOT NULL DEFAULT 0 CHECK (spent_total >= 0);
