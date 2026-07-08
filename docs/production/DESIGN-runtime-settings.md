# 运行时配置系统（app_settings + Redis 实时缓存）

> 建立于 2026-07-08。目标:让部分配置**后台可编辑、服务免重启生效、且可观测**——区别于 env(启动加载、改后需重启)。
> 首个接入项:Anthropic `anthropic-beta` 转发策略。代码在 `internal/service/appsettings/`。
> 第一批已落地(同日):6 组 gateway 热路径配置(熔断器/限流全局默认/流式 idle 超时/渠道 429 冷却/
> 凭据 401 阈值/默认渠道超时)从 env 迁入,经 `settingsApplier` 5s 周期推送热生效,
> 详见 `DESIGN-env-to-runtime-settings-migration.md`。

---

## 1. 为什么存在(与 env 的分工)

| | env 配置(`config.Load`) | 运行时配置(本系统) |
|---|---|---|
| 存储 | 环境变量 / `.env` | PostgreSQL `app_settings` 表 |
| 生效 | 进程启动时读一次,**改后必须重启** | **免重启**,跨进程秒级生效 |
| 适用 | 启动期一次性用掉的(端口/DB 池/监听地址/密钥) | 运行期每次现读的策略/阈值/开关 |
| 可观测 | 只读面板(`system_config.go`) | 后台可编辑 + 显示「当前生效值 + 来源」 |

**判断某配置该放哪的唯一标准:它是否"每次用时现读"。** 现读的(限流默认、beta 策略、流式静默超时、假定缓存率…)可进本系统并热改;
启动时一次性用掉的(HTTP 端口、DB 连接池、Redis 连接参数、master key)放 env,改了本就得重启,进本系统无意义。

## 2. 三层架构

```
                    ┌─────────────── admin-server ───────────────┐
  管理端保存 ──PUT──▶│ Service.SetRaw → SettingsStore.Set          │
                    │   1) 写 PostgreSQL app_settings(权威源)      │
                    │   2) 写 Redis  <ns>:settings:<key>(实时源)   │
                    │   3) 刷本地缓存                               │
                    └─────────────────────────────────────────────┘
                                      │ Redis(跨进程)
                    ┌─────────────── gateway-server ──────────────┐
  请求读策略 ───────▶│ Provider.BetaPolicy → SettingsStore.Raw     │
                    │   本地~3s缓存 → Redis实时源 → PG源 → 注册表默认│
                    └─────────────────────────────────────────────┘
```

- **存储层 · PostgreSQL `app_settings`**(`key TEXT PK, value JSONB, description, updated_at`):权威持久源。每个配置一行。
- **分发/缓存层 · Redis**(`<KeyNamespace>:settings:<key>`):跨进程实时源。admin 写完即刷,gateway 秒级读到;**值可直接 `redis-cli get` 观测**。
- **进程本地缓存**(~3s):热路径去抖,避免每请求打 Redis。因为很短,不牺牲"秒级生效"。

**两种消费模式**:
1. **每次现读**(如 beta 策略):消费方直接 `SettingsStore.Raw`,靠 Store 的三层缓存。
2. **applier 推送**(gateway 6 组热路径):`bootstrap/settings_applier.go` 每 5s 从 Store 拉取
   并经线程安全 setter 推给消费方(breaker/guard/cooldown/gate/router + adapter 包级 atomic),
   热路径零额外读取开销。改动生效延迟上限 ≈ 本地缓存 3s + applier 5s。

**启动 seed**(`SettingsStore.SeedDefaults`,gateway/admin bootstrap 均调用):遍历注册表,对 DB 缺行的
key 以 `INSERT … ON CONFLICT DO NOTHING` 写入默认值——绝不覆盖运维改过的值。注意语义:**seed 过的行即
"固化"**,后续版本升级代码默认值不会自动跟进 DB,靠后台「已偏离代码默认」标记提示人工决策。

**失败回退(读侧永不 error)**:本地 miss → Redis(挂了/miss)→ PG(挂了/无行)→ 注册表内置默认。任何一层失败只记 warn 并降级,
绝不因基础设施抖动让配置读取失败(这也修掉了"表缺失/DB 抖动直接 500"的坑)。
**写侧**:PG 写成功即算成功;Redis 刷新失败不阻断(下次读经 PG 兜底并回填 Redis)。

## 3. 配置注册表(通用化的关键)

`registry.go` 的 `Definition` 声明每个配置的元数据:

| 字段 | 含义 |
|---|---|
| `Key` | `app_settings` 主键,建议 `<domain>.<name>`(如 `anthropic.beta_policy`) |
| `Category` / `Label` / `Description` | 分组 / 标签 / 说明(写库时落 `description` 列,后台展示) |
| `HotReload` | 是否免重启生效(true=消费进程现读;false=仅展示,改后需重启) |
| `Default` | DB 无记录时的默认值(规范 JSON) |
| `Validate` | 写入前校验;非法值拒绝写入 |

admin 面板与后端读取都从注册表驱动,所以**新增配置几乎零样板**(见 §5)。

## 4. 可观测:如何验证"改的配置生效了"

进程内存缓存本身不可观测,故本系统用两个手段补齐:

1. **Redis 直查**:`redis-cli get <ns>:settings:<key>`(如 `unio:dev:settings:anthropic.beta_policy`)看到的就是跨进程实时值。
2. **`GET /admin/v1/settings`**:每项返回 `value` + `source`(`redis`/`db`/`default`)。`source=redis` 表示已跨进程传播;
   前端 beta 卡片底部的「当前生效」探针(5s 轮询)就用它——保存后看到来源变 `redis` 即确认生效。

## 5. 如何新增一个运行时配置项(操作手册)

以加一个假想的 `gateway.foo_ratio` 为例:

1. **定义值类型与 codec**:在合适的包里定好这个配置的 Go 类型 + JSON 编解码 + 校验(参考 `beta_policy.go` 的 `betaPolicyDoc` / `decodeBetaPolicy`)。
2. **注册**:在 `registry.go` 的 `DefaultRegistry()` 追加一条 `Definition`(key/分类/说明/默认/校验/是否热改)。
3. **消费端读取**:在用到它的地方通过 `SettingsStore.Raw(ctx, key)` 拿最新值并解码(热路径无需自己做缓存,Store 已管)。
   若像 beta 那样要注入到 core adapter,包一个 provider(参考 `BetaPolicyProvider` + `messagesadapter.SetBetaPolicyProvider`)。
4. **(可选)typed admin 入口**:通用 `PUT /admin/v1/settings/{key}` 已能写任意注册项;若要更友好的前端表单,像 beta 那样加一张卡片 + 专用端点。
5. **测试**:参考 `store_test.go`(默认/回写/校验/缓存去抖/错误回退)。

**无需**新建表或迁移(复用 `app_settings`),**无需**改分发/缓存代码(`SettingsStore` 通用)。

## 6. 边界与注意

- **非热改项不要谎报 `HotReload: true`**:端口/DB 池这类改了必须重启,若要纳入后台只能标 `HotReload:false` 做只读展示 + "需重启"提示,不给假承诺。
- **跨进程一致性**:Redis 是实时源,但各进程有 ~3s 本地缓存,故最终一致的窗口 ≈ 本地 TTL。需要更强即时性可改用 Redis pub/sub 失效通知(当前未做,YAGNI)。
- **admin 与 gateway 必须连同一个 Redis、同一 `REDIS_KEY_NAMESPACE`**,否则跨进程传播失效(退化为各自 DB 读 + 本地缓存,仍可用但非实时)。

## 7. 代码索引

- `internal/service/appsettings/registry.go` — 注册表 + `DefaultRegistry()`
- `internal/service/appsettings/store.go` — `SettingsStore`(三层读 / 写透 / 回退 / `Effective` 生效探针 / `SeedDefaults` 启动 seed)
- `internal/service/appsettings/beta_policy.go` — 首个接入项(定义 + provider + typed 读写)
- `internal/service/appsettings/gateway_settings.go` — 第一批 6 组 gateway 配置(类型 + codec + 校验 + Definition)
- `internal/service/appsettings/service.go` — admin 侧服务(通用 `List`/`SetRaw` + beta typed)
- `internal/app/adminapi/provider_settings.go` — admin API(`GET /settings`、`PUT /settings/{key}`、beta 专用端点)
- `internal/bootstrap/settings_applier.go` — gateway 后台 applier(5s 拉取 → 推给消费方)
- `internal/bootstrap/{gateway_server,admin_server}.go` — 装配注入(含 Redis、seed、applier 生命周期)
- 前端:`unio-admin/src/components/system/RuntimeGatewaySettings.tsx`(6 组编辑面板 + 偏离默认标记)
- 迁移:`000068_create_app_settings`、`000069_add_app_settings_description`
