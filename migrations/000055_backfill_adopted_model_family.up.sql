BEGIN;

-- 回填已采纳模型的 family。
--
-- 000054 给 model_catalog 加了 family 列，但当时目录内容还是空的，无法同时回填 models。
-- 目录同步跑过之后 model_catalog.family 才有值，此处把它带给早于该列采纳的模型，
-- 否则这些模型会一直显示「未归类」，直到有人手动触发一次「从目录刷新」。
--
-- 只覆盖当前为空的行：手动改过 family 的模型不应被目录结论推翻。
UPDATE models m
SET family = mc.family,
    updated_at = now()
FROM model_catalog_links l
JOIN model_catalog mc ON mc.canonical_id = l.canonical_id
WHERE l.model_id = m.id
  AND m.family = ''
  AND mc.family <> '';

COMMIT;
