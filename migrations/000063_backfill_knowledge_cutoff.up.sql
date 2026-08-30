-- 早期采纳的模型没有把目录条目的 knowledge_cutoff 快照进 models（当时该字段未随采纳拷贝），
-- console / website 详情的「知识截止」因此一直显示为空。现行采纳与「从目录刷新」均已拷贝，
-- 这里一次性回填存量：只填空值，不覆盖管理员手工编辑过的内容（幂等，可重复执行）。
UPDATE public.models m
SET knowledge_cutoff = c.knowledge_cutoff,
    updated_at = now()
FROM public.model_catalog_links l
JOIN public.model_catalog c ON c.canonical_id = l.canonical_id
WHERE l.model_id = m.id
  AND m.knowledge_cutoff = ''
  AND c.knowledge_cutoff <> '';
