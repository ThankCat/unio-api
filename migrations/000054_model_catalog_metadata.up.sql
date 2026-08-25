-- 模型目录元数据补齐：系列进目录表，出品方图标加格式约束与目录侧播种。
--
-- 这些字段只服务于展示：模型多到一定数量后，列表不按系列分组就没法看；
-- 出品方只有名字没有图标，扫一眼分不清谁是谁。它们不参与选路、计费或任何判定。
-- models.family 与 model_labs 表已由 000052 建立，这里补上目录侧的对应部分。

-- ── 1. family 进目录表 ───────────────────────────────────────────────────────
-- 有了它，「从目录采纳/刷新」才能把 family 一路带到 models，
-- 否则采纳完还要管理员手填一遍上游本来就给了的信息。
ALTER TABLE model_catalog ADD COLUMN family TEXT NOT NULL DEFAULT '';

COMMENT ON COLUMN model_catalog.family IS
    '模型系列（models.dev feed 的 family），采纳时带入 models.family，空串表示上游未归类。';

-- ── 2. 图标只接受 SVG ────────────────────────────────────────────────────────
-- 图标要能随主题改色、任意尺寸不失真，位图两条都做不到；
-- 约束放在库里而不只在同步任务里，是因为将来手工补图标也得守同一条规矩。
ALTER TABLE model_labs
    ADD CONSTRAINT ck_model_labs_logo_svg CHECK (
        logo_svg = '' OR logo_svg LIKE '%<svg%'
    );

-- ── 3. 目录侧的出品方一并播种 ────────────────────────────────────────────────
-- 000052 只用已采纳模型的 owned_by 播种，目录里尚未采纳的 lab 会缺行；
-- 缺行意味着这些出品方的图标永远同步不到，浏览目录时只能看到光秃秃的名字。
INSERT INTO model_labs (slug, name)
SELECT DISTINCT mc.lab, mc.lab
FROM model_catalog mc
WHERE mc.lab <> ''
ON CONFLICT (slug) DO NOTHING;
