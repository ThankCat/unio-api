# Unio Gateway Test 环境部署手册

本文档描述当前已落地的 Test 部署形态：单机 Docker Compose + 宿主机 Caddy + Cloudflare Flexible。  
仓库路径以服务器上 `/srv/unio-gateway`、本机构建机上的 `unio-admin` 为例；请按实际路径替换。

---

## 1. 架构与流量

```text
浏览器
  │  HTTPS（Cloudflare Universal SSL，证书覆盖 *.unioapi.com）
  ▼
Cloudflare（橙云代理，SSL 模式：Flexible）
  │  HTTP :80
  ▼
宿主机 Caddy（/etc/caddy/Caddyfile）
  │  reverse_proxy → 127.0.0.1:18080
  ▼
Compose 内 nginx（唯一映射到宿主机的业务入口）
  ├─ Host: test-api.unioapi.com
  │     /v1/*           → gateway:8520
  │     /healthz,/readyz→ gateway:8520
  │     /nginx-healthz  → 探活
  └─ Host: test-admin.unioapi.com
        /               → /var/www/admin（unio-admin 静态产物）
        /v1/*           → admin:8521
        /nginx-healthz  → 探活
```

| 组件 | 角色 | 端口 / 路径 |
|------|------|-------------|
| Cloudflare | 对外 HTTPS、隐藏源站 IP | 443（访客）→ 源站 80 |
| 宿主机 Caddy | 机器级大门，可与未来 website/console 共存 | `:80` → `127.0.0.1:18080` |
| Compose nginx | 按 Host 分流 Gateway / Admin | 宿主机 `127.0.0.1:18080` → 容器 `8080` |
| gateway | 公开 API | 容器内 `:8520`，路径 `/v1` |
| admin | Admin API | 容器内 `:8521`，路径 `/v1` |
| Admin 前端 | 静态 SPA，与 Admin API 同域 | 宿主机 `/var/www/admin` |
| postgres | 数据库 | 容器 `5432`；宿主机默认 `127.0.0.1:15432` |
| redis | 缓存 | 容器 `6379`；宿主机默认 `127.0.0.1:16379` |
| worker / loki / alloy | 后台与可观测 | 仅 Compose 网络内 |

**设计取舍（为何不让容器直接占 80）：**  
同一台机器后续还会跑 website、console 等。Compose 栈只占用本机高位端口 `18080`，由宿主机统一占 80/443，避免多栈抢端口。

**域名必须用单层子域：**  
Cloudflare 免费 Universal SSL 覆盖 `*.unioapi.com`（一层），**不覆盖** `test.api.unioapi.com` 这类两级子域。  
正确：`test-api.unioapi.com`、`test-admin.unioapi.com`。

---

## 2. 域名与 Cloudflare

### 2.1 DNS（A 记录，已代理 / 橙云）

| 名称 | 类型 | 内容 | 代理 |
|------|------|------|------|
| `test-api` | A | 服务器公网 IPv4 | 已代理 |
| `test-admin` | A | 同一公网 IPv4 | 已代理 |

完整主机名：

- Gateway：`https://test-api.unioapi.com`
- Admin：`https://test-admin.unioapi.com`

### 2.2 SSL/TLS

- 加密模式：**灵活（Flexible）**  
  访客 ↔ Cloudflare 为 HTTPS；Cloudflare ↔ 源站为 HTTP（打到宿主机 `:80`）。
- Universal SSL：保持开启；单层 `test-*` 会被 `*.unioapi.com` 覆盖。
- **现阶段不必**在源站配置 443 / Origin 证书。以后若改为 Full (strict)，再在宿主机上 HTTPS，并改 Caddy/证书。

### 2.3 防火墙

- 放行 **80**（供 Cloudflare 回源）
- **不要**对外放行 `18080`、`15432`、`16379`、`8520`、`8521`
- Postgres / Redis 默认只绑 `127.0.0.1`，本机管理工具用 **SSH 隧道** 连接（见第 9.3 节）

---

## 3. 仓库与分支要求

### 3.1 unio-gateway（服务器）

- 建议目录：`/srv/unio-gateway`
- 分支：`develop`（Test）
- `./deploy/prepare-image-env.sh` 要求：
  - 当前分支为 `develop` 或 `main`
  - 工作区干净
  - **HEAD 恰好有一个**合法 Git tag（同时作为 `IMAGE_TAG` / `IMAGE_VERSION`）

### 3.2 unio-admin（构建机，通常是本机）

- 使用 `bun run build:test`（`--mode test`）
- 读取 `.env.test`：`VITE_ADMIN_API_BASE=https://test-admin.unioapi.com`
- **不要**用 `bun run build:local` 或带 `.env.local` 本机地址的产物部署到服务器

---

## 4. 环境变量

### 4.1 创建文件

在服务器 `unio-gateway` 仓库根目录：

```bash
cd /srv/unio-gateway
cp deploy/env/.env.docker.example deploy/env/.env.docker
cp deploy/env/.env.test.example deploy/env/.env.test
chmod 600 deploy/env/.env.docker deploy/env/.env.test
```

### 4.2 `.env.test` —— 必须修改的密钥

将所有 `replace-with-...` 换成强随机值，且 **`DATABASE_URL` 中的密码必须与 `POSTGRES_PASSWORD` 完全一致**：

| 变量 | 说明 |
|------|------|
| `POSTGRES_PASSWORD` | Postgres 密码 |
| `DATABASE_URL` | 含同一密码；主机名保持 `postgres` |
| `REDIS_PASSWORD` | Redis 密码 |
| `ADMIN_PASSWORD` | Admin 控制台登录密码 |
| `GATEWAY_INTERNAL_TOKEN` | Gateway / Admin 内网诊断共用 token |

可用例如：

```bash
openssl rand -hex 32
```

### 4.3 `.env.test` —— 保持默认即可（与当前架构一致）

```bash
COMPOSE_PROJECT_NAME=unio_test
TEST_BIND_ADDRESS=127.0.0.1
NGINX_PUBLISHED_PORT=18080
POSTGRES_PUBLISHED_PORT=15432
REDIS_PUBLISHED_PORT=16379
GATEWAY_HTTP_ADDR=:8520
ADMIN_HTTP_ADDR=:8521
GATEWAY_INTERNAL_URLS=http://gateway:8520
```

域名写在 nginx / Caddy 配置里，**不必**写进 `.env.test`。

### 4.4 `.env.docker` —— 镜像版本用脚本生成

不要手填、也不要死抄本机旧的 `IMAGE_*`：

```bash
cd /srv/unio-gateway
./deploy/prepare-image-env.sh
```

脚本会写入：`IMAGE_TAG`、`IMAGE_VERSION`、`IMAGE_REVISION`、`IMAGE_CREATED`。

校验：

```bash
docker compose \
  --env-file deploy/env/.env.docker \
  --env-file deploy/env/.env.test \
  -f deploy/compose.test.yml \
  config >/dev/null && echo OK
```

---

## 5. Admin 静态目录

```bash
sudo mkdir -p /var/www/admin
sudo chown -R ubuntu:ubuntu /var/www/admin
sudo chmod 755 /var/www/admin
```

Compose 将 `/var/www/admin` **只读**挂进 nginx 容器。目录不存在时 Docker 可能建成 root 所属空目录，导致 rsync 失败或页面空白。

---

## 6. 首次启动 Compose

```bash
cd /srv/unio-gateway

docker compose \
  --env-file deploy/env/.env.docker \
  --env-file deploy/env/.env.test \
  -f deploy/compose.test.yml \
  build gateway admin worker migrate

docker compose \
  --env-file deploy/env/.env.docker \
  --env-file deploy/env/.env.test \
  -f deploy/compose.test.yml \
  up -d --no-build --wait
```

检查迁移：

```bash
docker compose \
  --env-file deploy/env/.env.docker \
  --env-file deploy/env/.env.test \
  -f deploy/compose.test.yml \
  logs migrate
```

查看状态：

```bash
docker compose \
  --env-file deploy/env/.env.docker \
  --env-file deploy/env/.env.test \
  -f deploy/compose.test.yml \
  ps
```

预期：`nginx` / `gateway` / `admin` / `postgres` / `redis` 为 healthy；网络名形如 `unio_test_network`；nginx 端口为 `127.0.0.1:18080->8080`。

---

## 7. 宿主机 Caddy

```bash
sudo apt update
sudo apt install -y caddy
sudo cp /srv/unio-gateway/deploy/caddy/Caddyfile.test /etc/caddy/Caddyfile
sudo cat /etc/caddy/Caddyfile
sudo systemctl enable --now caddy
sudo systemctl reload caddy
```

确认文件内容为（域名必须是带 `-` 的单层名）：

```caddy
http://test-api.unioapi.com, http://test-admin.unioapi.com {
	reverse_proxy 127.0.0.1:18080
}
```

**不要**与宿主机 Nginx 同时占用 `:80`。  
每次改仓库里的 `Caddyfile.test` 后，必须重新 `cp` 到 `/etc/caddy/Caddyfile` 再 `reload`，否则会出现 Host 不匹配、返回空 body。

---

## 8. 构建并发布 Admin 前端

在**构建机**（本机）：

```bash
cd /path/to/unio-admin
bun run build:test

# 确认烤进的是 Test 域名，且没有本机地址
rg -n 'https://test-admin\.unioapi\.com' dist/assets
rg -n '127\.0\.0\.1:8521|test\.admin\.unioapi' dist && echo 'ERROR: bad API base' || echo 'OK'

rsync -a --delete dist/ ubuntu@<服务器IP>:/var/www/admin/
```

服务器上确认：

```bash
ls -la /var/www/admin/
wc -c /var/www/admin/index.html
head -20 /var/www/admin/index.html
```

`index.html` 应含 `<div id="root">` 与 `/assets/...` 引用；文件大小不应为 0。

---

## 9. 验证清单

### 9.1 服务器本机（不经 Cloudflare）

```bash
# Compose nginx
curl -sS -H 'Host: test-api.unioapi.com' http://127.0.0.1:18080/nginx-healthz
curl -sS -H 'Host: test-admin.unioapi.com' http://127.0.0.1:18080/nginx-healthz
curl -sS -H 'Host: test-api.unioapi.com' http://127.0.0.1:18080/readyz
curl -sS -o /dev/null -w '%{http_code}\n' -H 'Host: test-admin.unioapi.com' http://127.0.0.1:18080/
curl -sS -H 'Host: test-admin.unioapi.com' http://127.0.0.1:18080/ | wc -c

# 经 Caddy :80
curl -sS -H 'Host: test-api.unioapi.com' http://127.0.0.1:80/nginx-healthz
curl -sS -H 'Host: test-admin.unioapi.com' http://127.0.0.1:80/nginx-healthz
curl -sS -H 'Host: test-admin.unioapi.com' http://127.0.0.1:80/ | wc -c
```

期望：`ok`、`{"status":"ready"}`、Admin 根路径 HTTP `200`、HTML 字节数约等于 `index.html` 大小（非 0）。

### 9.2 公网

```bash
curl -sS https://test-api.unioapi.com/nginx-healthz
curl -sS https://test-admin.unioapi.com/nginx-healthz
curl -sS -o /dev/null -w '%{http_code}\n' https://test-admin.unioapi.com/
```

浏览器：

- 打开 `https://test-admin.unioapi.com/`（注意是 **`test-admin`**，不是 `test.admin`）
- 登录请求应为 `POST https://test-admin.unioapi.com/v1/login`
- 账号：`.env.test` 中的 `ADMIN_USERNAME` / `ADMIN_PASSWORD`

Gateway API 示例：`https://test-api.unioapi.com/v1/...`

### 9.3 本机管理工具连接 Postgres / Redis（SSH 隧道）

Compose 将数据端口映射到服务器本机：

| 服务 | 服务器本机地址 |
|------|----------------|
| Postgres | `127.0.0.1:15432` → 容器 `5432` |
| Redis | `127.0.0.1:16379` → 容器 `6379` |

在你的 Mac 上开隧道（保持终端不关）：

```bash
ssh -N \
  -L 15432:127.0.0.1:15432 \
  -L 16379:127.0.0.1:16379 \
  ubuntu@<服务器公网IP>
```

然后管理工具连本机：

| | Host | Port | 认证 |
|--|------|------|------|
| Postgres（TablePlus / DBeaver 等） | `127.0.0.1` | `15432` | 用户/库/密码见 `.env.test` 的 `POSTGRES_*` |
| Redis（Another Redis Desktop Manager 等） | `127.0.0.1` | `16379` | 密码为 `REDIS_PASSWORD` |

服务器上快速自检：

```bash
ss -tlnp | grep -E '15432|16379'
# 应看到 127.0.0.1:15432 与 127.0.0.1:16379
```

**不要**把 `TEST_BIND_ADDRESS` 改成 `0.0.0.0` 并把 15432/16379 对公网开放；数据库被扫到的风险很高。

---

## 10. 日常更新

### 10.1 只更新后端镜像

```bash
cd /srv/unio-gateway
git pull
# 工作区干净且 HEAD 有 tag 时：
./deploy/prepare-image-env.sh

docker compose \
  --env-file deploy/env/.env.docker \
  --env-file deploy/env/.env.test \
  -f deploy/compose.test.yml \
  build gateway admin worker migrate

docker compose \
  --env-file deploy/env/.env.docker \
  --env-file deploy/env/.env.test \
  -f deploy/compose.test.yml \
  up -d --no-build --wait
```

### 10.2 只更新 nginx / Caddy 配置

```bash
cd /srv/unio-gateway
git pull

docker compose \
  --env-file deploy/env/.env.docker \
  --env-file deploy/env/.env.test \
  -f deploy/compose.test.yml \
  up -d --force-recreate --no-deps nginx

sudo cp deploy/caddy/Caddyfile.test /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

### 10.3 只更新 Admin 前端

```bash
# 构建机
cd /path/to/unio-admin
git pull
bun run build:test
rsync -a --delete dist/ ubuntu@<服务器IP>:/var/www/admin/
```

浏览器强制刷新或无痕访问。

### 10.4 停止（保留数据卷）

```bash
docker compose \
  --env-file deploy/env/.env.docker \
  --env-file deploy/env/.env.test \
  -f deploy/compose.test.yml \
  down
```

仅在确认可丢弃 Test 数据时才使用 `down --volumes`。

---

## 11. 关键路径速查

| 路径 | 用途 |
|------|------|
| `deploy/compose.test.yml` | Test Compose 编排 |
| `deploy/nginx/test.conf` | 容器内 nginx（按 Host 分流） |
| `deploy/caddy/Caddyfile.test` | 宿主机 Caddy 模板 → 复制到 `/etc/caddy/Caddyfile` |
| `deploy/env/.env.test` | 业务 / 密钥（gitignore） |
| `deploy/env/.env.docker` | 镜像名与版本（gitignore） |
| `deploy/prepare-image-env.sh` | 从 Git tag 写入 `IMAGE_*` |
| `/var/www/admin` | Admin 前端静态文件 |
| `unio-admin/.env.test` | 前端 Test 构建 API 基址 |
| `unio-admin`：`bun run build:test` | 产出部署用 `dist/` |

---

## 12. 故障排查

| 现象 | 常见原因 | 处理 |
|------|----------|------|
| `ERR_SSL_VERSION_OR_CIPHER_MISMATCH` | 使用了两级子域 `test.api` / `test.admin` | 改用 `test-api` / `test-admin`，更新 DNS 与配置 |
| 公网 HTTPS 空 body、本机 18080 正常 | Caddy 仍是旧 `server_name`，Host 未命中 | `cp Caddyfile.test` 后 `reload`，再测 `:80` |
| 页面请求 `127.0.0.1:8521` | 用了 local 构建产物 | `bun run build:test` 后重新 rsync |
| 请求仍打 `test.admin.unioapi.com` | 浏览器缓存了旧 JS | 重新 `build:test` + rsync + 强刷 |
| 页面空白且 `index.html` 为 0 或缺文件 | `/var/www/admin` 未同步或权限不对 | `chown ubuntu` 后 rsync |
| rsync Permission denied | 目录属 root | `sudo chown -R ubuntu:ubuntu /var/www/admin` |
| `prepare-image-env.sh` 失败 | 非 develop/main、脏工作区、或 HEAD 无唯一 tag | 按脚本报错处理 |
| 本机 curl HTTPS 异常但他人正常 | 本机代理 fake-ip（如 `198.18.x`） | 用手机流量或服务器侧验证；非服务端故障 |

分层探测顺序：

1. `127.0.0.1:18080` + `Host` 头  
2. `127.0.0.1:80` + `Host` 头（Caddy）  
3. 公网 `https://test-*.unioapi.com`

---

## 13. 与本地开发的区别

| | 本地 `make dev` | Test 部署 |
|--|-----------------|-----------|
| Gateway | `:8520`，路径 `/v1` | 同左，经 `test-api` 暴露 |
| Admin API | `:8521`，路径 `/v1` | 同左，经 `test-admin` 暴露 |
| Admin 前端 | Vite dev，`.env.local` → `http://127.0.0.1:8521` | 静态托管，`.env.test` → `https://test-admin.unioapi.com` |
| 入口 | 直连端口 | Cloudflare → Caddy → compose nginx |
| 数据 | 本地 Compose / 本机 DB | `COMPOSE_PROJECT_NAME=unio_test` 隔离卷 |

本地开发继续用仓库根目录 `.env` 与 `make dev` / `bun dev`；Test 只使用 `deploy/env/*` 与 `build:test` 产物。
