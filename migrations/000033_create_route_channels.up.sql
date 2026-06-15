-- Route channel 是自定义线路（pool_kind='explicit'）的渠道池成员（阶段 15）。
-- pool_kind='all' 的线路不得有 route_channels 行；mode='fixed' 必须恰好一条（由 service 层强校验）。
CREATE TABLE route_channels (
    -- route_id: 所属线路，线路删除时级联清理。--
    route_id BIGINT NOT NULL REFERENCES routes (id) ON DELETE CASCADE,

    -- channel_id: 纳入该线路候选池的 channel ID。--
    channel_id BIGINT NOT NULL REFERENCES channels (id),

    -- 同一线路对同一渠道只能登记一次。--
    PRIMARY KEY (route_id, channel_id)
);

-- 渠道删除 / 反查某渠道属于哪些线路时按 channel_id 扫描。
CREATE INDEX idx_route_channels_channel ON route_channels (channel_id);
