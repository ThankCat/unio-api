# Phase 6 Acceptance

## 功能验收

1. 数据库能表达 provider、channel、model、channel_model。
2. routing 可以选择可用 channel。
3. gateway 根据 routing 结果调用 adapter。
4. `/v1/models` 来自 model catalog。

## 生产验收

1. provider/channel/model/price 不作为正式 env/config 来源。
2. credential_ref 不保存长期明文 key。
3. 启动时校验 adapter registry 与 `channel.protocol + channel.adapter_key` 复合绑定一致。
4. routing 支持 project 级模型可见性；预算和后台管理策略已登记到后续阶段。
5. routing 错误可区分模型不存在和无可用 channel。

## 测试验收

1. model catalog 查询测试通过。
2. routing 选择、fallback 候选和错误测试通过。
3. credential resolver 测试覆盖开发期 static resolver。
4. 正式 resolver 实现时补充解密、轮换和失败测试。

## 文档验收

1. provider/channel/model/channel_model 边界写入章节文档。
2. credential_ref 最终方案与生产 TODO 对齐。
