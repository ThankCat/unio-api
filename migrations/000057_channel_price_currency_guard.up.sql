-- 一渠道一币种守卫（多货币 Phase 2，PLAN M4/D3）：channel_prices.currency 必须等于所属
-- provider.currency。供应商账单以什么货币计价，渠道成本就以什么货币录入——由此同一渠道×模型
-- 不可能出现 CNY / USD 两行并存的启用价格，路由候选查询无需按币种过滤（F7 隐患消除）。
-- provider.currency 的不可变性（有 channel_prices / 账本 / 余额引用后禁止修改）由应用层强制。
CREATE OR REPLACE FUNCTION public.assert_channel_price_currency_matches_provider()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = public, pg_temp
AS $$
DECLARE
    provider_currency text;
BEGIN
    SELECT p.currency INTO provider_currency
    FROM providers p
    JOIN channels c ON c.provider_id = p.id
    WHERE c.id = NEW.channel_id;

    IF provider_currency IS NULL THEN
        -- channel/provider 不存在时交给外键约束报错，这里不越权。
        RETURN NULL;
    END IF;
    IF NEW.currency <> provider_currency THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            CONSTRAINT = 'ck_channel_price_currency_matches_provider',
            MESSAGE = format(
                'channel_prices.currency=%s 与 provider.currency=%s 不一致（channel=%s）',
                NEW.currency, provider_currency, NEW.channel_id
            );
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER trg_channel_prices_currency_guard
AFTER INSERT OR UPDATE ON channel_prices DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION assert_channel_price_currency_matches_provider();
