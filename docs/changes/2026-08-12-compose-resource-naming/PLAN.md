# Compose 资源命名统一计划

## 目标

统一 Dev/Test 的 Docker Compose 项目、容器、网络和卷命名：项目名使用
`unio-dev` / `unio-test`，容器由 Compose 自动生成，网络和卷显式使用全小写连字符名称。

## 范围

1. Dev 移除固定 `container_name` 和外部卷预创建逻辑，使用 Compose 管理的
   `unio-dev-*-data` 卷及 `unio-dev-network` 网络。
2. 清空并重建本机 Dev 基础设施，验证资源名称和服务健康状态。
3. Dev 验证通过后，将同一规则应用到 Test 配置，但不启动 Test、不迁移 Test 数据。
4. 更新依赖旧 Dev 容器名的本地脚本及部署说明。

## 验证

1. 对 Dev/Test 执行 `docker compose config` 和 `git diff --check`。
2. Dev 执行 `docker compose up -d --wait`，核对容器、网络、卷实际名称。
3. 验证 PostgreSQL、Redis healthcheck，以及 Loki `/ready` 和 Alloy HTTP 就绪状态。

## 约束

- Dev 数据无需保留，允许删除旧 Dev 容器、网络和卷。
- Test 数据后续单独迁移，本次不启动 Test、不删除或迁移任何 Test 资源。
- 本次不提交、不推送代码，不操作远程环境。
