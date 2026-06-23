-- 移除能力架构 Layer 3「渠道能力收紧」（DEC-023）。
--
-- 决策：默认所有渠道对模型层声明的能力均完美支持，不再提供 per-channel 收紧。能力判定只看模型层
-- （model_capabilities）。详见 DECISIONS.md DEC-023 / DEC-015 批注。
DROP TABLE IF EXISTS channel_capability_overrides;
