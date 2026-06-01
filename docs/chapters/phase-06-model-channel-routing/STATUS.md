# Phase 6 Status

状态：done

## 已完成与承接

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| TASK-6.01 | done | provider/channel/model/channel_model schema 和基础查询已完成。 |
| TASK-6.02 | deferred | 正式 credential resolver 推迟到阶段 11 前置。 |
| TASK-6.03 | done | adapter registry、credential/routing、gateway/settlement、HTTP handler/rate limit、server app wiring 和启动期 provider.adapter preflight 已迁入 `internal/bootstrap`；阶段 10 升级 channel protocol 后，后台写入校验推迟到阶段 11 provider/channel CRUD。 |
| TASK-6.04 | done | project 模型 allow-list/deny-list 已接入 routing 和 `/v1/models`；预算约束已进入阶段 7 reservation/余额冻结，project 禁用和专属 channel 策略推迟到阶段 11。 |
| TASK-6.05 | done | routing 已区分模型不存在、project 不可用和无可用 channel；HTTP handler 已映射为安全 OpenAI-compatible 错误。 |

## 下一次进入本阶段前必须检查

```bash
rg -n "GAP-6-" docs/production/TODO_REGISTER.md cmd internal migrations sql
```
