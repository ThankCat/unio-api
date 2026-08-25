BEGIN;

-- 停用「已启用但没有任何可用渠道」的模型。
--
-- 模型中心化改造建立了不变量：启用的模型必须至少有一条可用渠道（渠道启用 + 绑定启用）。
-- 该检查发生在启用与解绑路径上，管不到在它之前就已经处于违规状态的存量行。
-- 这些行会照常出现在 /v1/models 里，客户选中后必然拿到 503——列表里看得见、
-- 一调就失败是最难排查的一类问题，所以在此把它们对齐到不变量。
--
-- 理由记为 binding_disabled：它们没有可用绑定，与「渠道整条停用」不是一回事。
UPDATE models m
SET status = 'disabled',
    disabled_reason = 'binding_disabled',
    disabled_at = now(),
    updated_at = now()
WHERE m.status = 'enabled'
  AND NOT EXISTS (
      SELECT 1
      FROM channel_models cm
      JOIN channels c ON c.id = cm.channel_id
      WHERE cm.model_id = m.id
        AND cm.status = 'enabled'
        AND c.status = 'enabled'
  );

COMMIT;
