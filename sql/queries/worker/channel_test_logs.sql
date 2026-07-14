-- name: DeleteChannelTestLogsBeyondPerChannel :execrows
-- DeleteChannelTestLogsBeyondPerChannel 保留每渠道最近 keep 条，删除更旧的（R1：默认 200）。
-- worker 每轮末尾对本轮涉及的渠道逐一调用；用窗口函数按 (channel_id) 分区、created_at/id 倒序打名次，删名次 > keep 的行。
DELETE FROM channel_test_logs AS del
WHERE del.channel_id = sqlc.arg(channel_id)
  AND del.id NOT IN (
      SELECT ctl.id
      FROM channel_test_logs ctl
      WHERE ctl.channel_id = sqlc.arg(channel_id)
      ORDER BY ctl.created_at DESC, ctl.id DESC
      LIMIT sqlc.arg(keep)
  );
