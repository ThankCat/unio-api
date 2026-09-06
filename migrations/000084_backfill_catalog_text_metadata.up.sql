-- 回填被「状态开关」误清空的采纳元数据（2026-09-06）。
--
-- Admin 列表页的启用/停用开关走完整更新接口，但请求体没带 description / knowledge_cutoff；
-- 接口此前把缺省字段按空串整字段覆盖，于是每点一次开关就把采纳来的简介与知识截止清一次。
-- 接口已改为「缺省即不改」，这里一次性把存量空值按 model_catalog_links 从目录回填：
-- 只填空值、不覆盖管理员手工编辑过的内容（幂等，可重复执行）。
UPDATE public.models m
SET description = CASE WHEN m.description = '' THEN c.description ELSE m.description END,
    knowledge_cutoff = CASE WHEN m.knowledge_cutoff = '' THEN c.knowledge_cutoff ELSE m.knowledge_cutoff END,
    updated_at = now()
FROM public.model_catalog_links l
JOIN public.model_catalog c ON c.canonical_id = l.canonical_id
WHERE l.model_id = m.id
  AND (
    (m.description = '' AND c.description <> '')
    OR (m.knowledge_cutoff = '' AND c.knowledge_cutoff <> '')
  );
