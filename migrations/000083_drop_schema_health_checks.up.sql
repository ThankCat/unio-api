-- 退役开发期迁移验证表（2026-09-06）。
--
-- schema_health_checks 由 000028 为「验证 migration 流程已经跑通」而建，不承载业务含义；
-- 部署已统一使用 golang-migrate，其自带 schema_migrations 版本表承担同一职责。
-- 该表在 Go 代码中零引用（对应查询与用例同批删除），此处直接删除。
DROP TABLE IF EXISTS public.schema_health_checks CASCADE;
