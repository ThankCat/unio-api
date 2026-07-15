-- 回滚:拆回独立 key(取自聚合对象字段;聚合行本身保留无害,下一次 seed/部署会再合并)。

INSERT INTO public.app_settings (key, value, description)
SELECT
  'admin_backend.channel_test_probe_timeout_ms',
  COALESCE(value -> 'probe_timeout_ms', '60000'::jsonb),
  '渠道检测超时(毫秒);已拆自 admin_backend.channel_test。'
FROM public.app_settings
WHERE key = 'admin_backend.channel_test'
ON CONFLICT (key) DO NOTHING;

INSERT INTO public.app_settings (key, value, description)
SELECT
  'admin_backend.channel_test_worker_enabled',
  COALESCE(value -> 'enabled', 'true'::jsonb),
  '渠道自动巡检开关;已拆自 admin_backend.channel_test。'
FROM public.app_settings
WHERE key = 'admin_backend.channel_test'
ON CONFLICT (key) DO NOTHING;

INSERT INTO public.app_settings (key, value, description)
SELECT
  'admin_backend.channel_test_worker_interval_ms',
  COALESCE(value -> 'interval_ms', '1800000'::jsonb),
  '渠道巡检间隔(毫秒);已拆自 admin_backend.channel_test。'
FROM public.app_settings
WHERE key = 'admin_backend.channel_test'
ON CONFLICT (key) DO NOTHING;

INSERT INTO public.app_settings (key, value, description)
SELECT
  'admin_backend.channel_test_log_retention_per_channel',
  COALESCE(value -> 'log_retention_per_channel', '200'::jsonb),
  '每渠道检测日志保留条数;已拆自 admin_backend.channel_test。'
FROM public.app_settings
WHERE key = 'admin_backend.channel_test'
ON CONFLICT (key) DO NOTHING;

DELETE FROM public.app_settings WHERE key = 'admin_backend.channel_test';
