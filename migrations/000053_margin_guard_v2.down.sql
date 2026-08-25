-- 卸掉模型为中心的毛利守卫。000037 的线路版守卫由 000052 的 down 迁移负责重建。
DROP TRIGGER IF EXISTS trg_models_margin_guard ON models;
DROP TRIGGER IF EXISTS trg_channels_margin_guard ON channels;
DROP TRIGGER IF EXISTS trg_providers_margin_guard ON providers;
DROP TRIGGER IF EXISTS trg_channel_models_margin_guard ON channel_models;
DROP TRIGGER IF EXISTS trg_model_prices_margin_guard ON model_prices;
DROP TRIGGER IF EXISTS trg_model_price_service_tiers_margin_guard ON model_price_service_tiers;
DROP TRIGGER IF EXISTS trg_channel_prices_margin_guard ON channel_prices;
DROP TRIGGER IF EXISTS trg_channel_price_service_tiers_margin_guard ON channel_price_service_tiers;
DROP TRIGGER IF EXISTS trg_channel_cost_multipliers_margin_guard ON channel_cost_multipliers;
DROP TRIGGER IF EXISTS trg_channel_recharge_factors_margin_guard ON channel_recharge_factors;
DROP TRIGGER IF EXISTS trg_app_settings_sale_ratio_margin_guard ON app_settings;
DROP FUNCTION IF EXISTS public.assert_non_negative_margins();
