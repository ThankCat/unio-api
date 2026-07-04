-- 为 channel 增加「凭据是否有效」闸门列（阶段二：真摘除 + 检测通过才恢复）。
-- credential_valid=false 表示系统判定该渠道凭据失效（连续 401 或检测判定 credential_invalid），
-- 与 status（管理员启停意图）正交：即使 status='enabled'，credential_valid=false 也不参与路由候选。
-- 翻失效/翻有效的「何时/为何/每次检测结果」历史记入 channel_test_logs（000063），此列只存当前布尔。
ALTER TABLE channels
    ADD COLUMN credential_valid BOOLEAN NOT NULL DEFAULT TRUE;

-- 供 worker 快速捞出失效渠道复检（部分索引，仅索引失效行，体积小）。
CREATE INDEX IF NOT EXISTS idx_channels_credential_invalid
    ON channels (id)
    WHERE credential_valid = FALSE;
