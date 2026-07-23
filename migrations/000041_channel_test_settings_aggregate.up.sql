-- 将渠道巡检相关拆分 key 合并为 admin_backend.channel_test 聚合对象。
-- 若旧 key 已有自定义值则带入新对象；随后删除旧 key（面板不再展示）。

INSERT INTO public.app_settings (key, value, description)
SELECT
  'admin_backend.channel_test',
  jsonb_build_object(
    'enabled',
    COALESCE(
      (SELECT value FROM public.app_settings WHERE key = 'admin_backend.channel_test_worker_enabled'),
      'true'::jsonb
    ),
    'interval_ms',
    COALESCE(
      (SELECT value FROM public.app_settings WHERE key = 'admin_backend.channel_test_worker_interval_ms'),
      '1800000'::jsonb
    ),
    'probe_timeout_ms',
    COALESCE(
      (SELECT value FROM public.app_settings WHERE key = 'admin_backend.channel_test_probe_timeout_ms'),
      '60000'::jsonb
    ),
    'log_retention_per_channel',
    COALESCE(
      (SELECT value FROM public.app_settings WHERE key = 'admin_backend.channel_test_log_retention_per_channel'),
      '200'::jsonb
    )
  ),
  '渠道凭据检测与自动巡检的聚合配置:开关、巡检间隔、探测超时、每渠道日志保留条数。'
WHERE NOT EXISTS (
  SELECT 1 FROM public.app_settings WHERE key = 'admin_backend.channel_test'
);

DELETE FROM public.app_settings
WHERE key IN (
  'admin_backend.channel_test_probe_timeout_ms',
  'admin_backend.channel_test_worker_enabled',
  'admin_backend.channel_test_worker_interval_ms',
  'admin_backend.channel_test_log_retention_per_channel'
);
