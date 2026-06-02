-- Usage line item 是一次 usage record 下受控的附加计量事实。
CREATE TABLE usage_line_items (
    -- id: 主键。--
    id BIGSERIAL PRIMARY KEY,

    -- usage_record_id: 所属 usage record ID。--
    usage_record_id BIGINT NOT NULL REFERENCES usage_records (id),

    -- kind: 已登记的附加计量项类型。--
    kind TEXT NOT NULL CHECK (
        kind IN ('server_web_search_request', 'server_web_fetch_request')
    ),

    -- quantity: 本次请求发生的附加计量数量。--
    quantity BIGINT NOT NULL CHECK (quantity > 0),

    -- created_at: 记录创建时间。--
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- 同一 usage record 下每种受控计量项只能有一条汇总事实。--
    CONSTRAINT uq_usage_line_items_record_kind UNIQUE (usage_record_id, kind)
);

-- 请求详情会按 usage record 查找完整附加计量事实。
CREATE INDEX idx_usage_line_items_usage_record_id ON usage_line_items (usage_record_id, id);
