-- name: GetModelLabLogo :one
-- GetModelLabLogo 读取出品方图标 SVG（console 与 admin 的 logo 端点共用）。
-- 空串表示登记过但上游没有图标；无行表示 slug 未登记——两者都由上层按 404 处理。
SELECT logo_svg
FROM model_labs
WHERE slug = sqlc.arg(slug);
