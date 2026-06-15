-- Model catalog link 记录「运营模型 ← 采纳来源目录条目」的关联与更新提醒状态（阶段 14）。
-- 采纳是一次快照拷贝；目录之后变化不自动回灌，靠指纹对比检测「有更新」并提醒。
CREATE TABLE model_catalog_links (
    -- model_id: 关联的运营模型 ID；一个模型至多关联一条目录条目，模型删除时级联清理。--
    model_id BIGINT PRIMARY KEY REFERENCES models (id) ON DELETE CASCADE,

    -- canonical_id: 采纳来源目录条目；非唯一 → 一条目录可派生多个模型（Q2）。--
    canonical_id TEXT NOT NULL REFERENCES model_catalog (canonical_id),

    -- adopted_fingerprint: 采纳/上次刷新时目录条目的 fingerprint（快照基线）。--
    adopted_fingerprint TEXT NOT NULL CHECK (adopted_fingerprint <> ''),

    -- reminder_muted: 永久忽略更新（true 表示不再提醒，可取消静音）。--
    reminder_muted BOOLEAN NOT NULL DEFAULT false,

    -- reminder_snooze_until: 稍后提醒：此时间之前不提醒，空表示未稍后。--
    reminder_snooze_until TIMESTAMPTZ,

    -- dismissed_fingerprint: 忽略本次更新所对应的目录 fingerprint；目录再变到新指纹会重新提醒。--
    dismissed_fingerprint TEXT,

    -- created_at: 记录创建时间。--
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- updated_at: 记录更新时间。--
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 目录条目被采纳计数 / 反查派生模型按 canonical_id 查询。
CREATE INDEX idx_model_catalog_links_canonical ON model_catalog_links (canonical_id);
