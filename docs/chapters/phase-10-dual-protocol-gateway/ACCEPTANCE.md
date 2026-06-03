# Phase 10 Acceptance

## 产品验收

1. OpenAI 客户只修改 `base_url` 和 `api_key`，可以调用 `POST /v1/chat/completions`。
2. Anthropic 客户只修改 `base_url` 和 `api_key`，可以调用 `POST /v1/messages`。
3. DeepSeek 同一个 provider 可以配置 OpenAI 与 Anthropic 两套 channel。
4. OpenAI ingress 只路由到 OpenAI channel，Anthropic ingress 只路由到 Anthropic channel。
5. 两套协议共享身份、routing、authorization、fallback、settlement、recovery、metrics 和 tracing。
6. 本阶段只承诺双协议对话链路和字段显式行为，不承诺图片、视频、音频、文件等
   模型能力扩展；provider 无法转换的合法协议字段在 adapter 出站 **Drop**，ingress 不 400。

## OpenAI 协议验收

1. [OPENAI_CHAT_COMPLETIONS_MATRIX.md](OPENAI_CHAT_COMPLETIONS_MATRIX.md) 的请求顶层字段全部为 `Typed` 或已登记 `Passthrough`；provider **Drop** 见 [DEEPSEEK_OPENAI_MAPPING.md](DEEPSEEK_OPENAI_MAPPING.md)。
2. messages role、content union、多模态、audio、file、legacy function、tools 和 structured output 在 ingress 有显式 DTO；出站 Drop 有 mapping 登记。
3. 非流式 response 的 choices、message、logprobs、annotations、audio、tool_calls 和 usage details 完整（协议需要的字段）。
4. 流式 chunk、delta、usage 尾包、错误 chunk 和 `[DONE]` 完整。
5. DeepSeek OpenAI mapping 没有残留 `Verify`。
6. 多模态、audio、file、web search 等字段 typed 化不等于能力支持；当前 provider
   不支持时在 upstream body **Drop**，客户仍走合法 OpenAI 协议路径（不因 provider 能力 400）。

## Anthropic 协议验收

1. [ANTHROPIC_MESSAGES_MATRIX.md](ANTHROPIC_MESSAGES_MATRIX.md) 的顶层字段全部为 `Typed` 或已登记 `Passthrough`；provider **Drop** 见 [DEEPSEEK_ANTHROPIC_MAPPING.md](DEEPSEEK_ANTHROPIC_MAPPING.md)。
2. messages content block union、thinking、tool choice、tools、cache control 和 output config 在 ingress 有显式行为；出站 Drop 有 mapping 登记。
3. 非流式 Message response、content block、stop reason、usage 完整。
4. 流式 named SSE event 与 delta union 完整。
5. DeepSeek Anthropic mapping 没有残留 `Verify`。
6. image、document、container upload 等 block typed 化不等于能力支持；当前 provider
   不支持时在 upstream body **Drop**，ingress 不因 provider 能力 400。

## Adapter 验收

1. `adapter/openai/deepseek` 完成请求、非流式响应、流式响应、usage、error 和内部输入 tokenizer。
2. `adapter/openai/deepseek` 生产代码开始前，
   [DEEPSEEK_OPENAI_MAPPING.md](DEEPSEEK_OPENAI_MAPPING.md) 已通过黑盒冻结且无 `Verify`。
3. `adapter/anthropic/deepseek` 完成请求、非流式响应、流式响应、usage、error 和内部输入 tokenizer。
4. `adapter/anthropic/deepseek` 生产代码开始前，
   [DEEPSEEK_ANTHROPIC_MAPPING.md](DEEPSEEK_ANTHROPIC_MAPPING.md) 已通过黑盒冻结且无 `Verify`。
5. adapter 不读取 DB/env，不保存业务状态。
6. adapter 一次调用只发送一次真实 upstream HTTP 请求。
7. 生产 adapter 不依赖 OpenAI 或 Anthropic 官方 Go SDK。
8. 原 `adapter/openai/streamtranslate` 已移除。
9. tokenizer 只用于 authorization 预估，不新增公网 tokenizer endpoint。
10. 非流式、流式和 input tokenizer capability 独立注册；缺失能力的 channel 不参与对应 routing。
11. 不存在 `FullChatAdapter`、`FullMessagesAdapter` 等强制组合接口。
12. OpenAI 与 Anthropic tokenizer 分别消费各自协议 DTO；共享 lifecycle 只调用候选级
    估算 closure，不依赖协议 DTO。
13. DeepSeek 在 `adapter/openai/deepseek/tokenizer.go` 与
    `adapter/anthropic/deepseek/tokenizer.go` 分别实现 tokenizer；不存在共享 provider
    tokenizer facade 或跨协议 tokenizer 中间 DTO。

## ResponseFacts 与账务验收

1. 客户协议响应与 `ResponseFacts` 在同一次解析中产生。
2. `usage.Facts` 区分 known、not_applicable 和 unknown。
3. OpenAI cache read、Anthropic cache read/cache write 5m/cache write 1h 都能表达。
4. output total 与 reasoning output 不会重复收费。
5. server tool usage 有受控 line item，不使用任意 JSON key 参与计费。
6. request、attempt、usage、价格快照、成本快照和 recovery job 可审计。
7. worker 只重放 immutable facts，不重新解析 response body。
8. 非流式响应只在 immutable recovery facts 已持久化后返回；首次 settlement 失败但
   durable recovery job 已接管时记录 pending recovery，不丢失账务事实。

## Stream 验收

1. 首个客户可见事件之前允许 fallback。
2. 首个客户可见事件之后禁止 fallback。
3. 有可靠 usage 的 tail error 或客户端取消仍按事实 settlement。
4. 没有可靠 usage 但可能产生上游成本时写 `risk_exposure`。
5. `delivery_status` 与 upstream、settlement 状态分离。
6. OpenAI 与 Anthropic 流式错误分别使用协议原生形状。
7. adapter 不直接透出 upstream `[DONE]` 或 `message_stop`；facts 持久化且 settlement
   成功或 durable recovery job 接管后，gatewayapi writer 才输出成功终态。
8. recovery facts 无法持久化时不输出成功终态，已开始的 stream 输出协议原生 error
   event 并记录 delivery interrupted。
9. 客户端断开后的账务收口使用有上限的内部 context。
10. OpenAI final usage 先进入 durable closeout，再按 `include_usage` 输出客户可选尾包，
    最后写 `[DONE]`。

## 安全验收

1. provider 原始错误 body 不直接返回客户。
2. OpenAI 和 Anthropic 错误响应分别由对应 gatewayapi 包渲染。
3. API key、credential、prompt 和完整响应正文默认不进入审计表。
4. `anthropic-beta` 一律接受（含未登记值与逗号分隔多值），不因 provider 能力 400；按 [DEC-013](../../production/DECISIONS.md#dec-013-协议-beta-header-宽进接受与出站-drop) 在 provider 映射层 Drop（当前 DeepSeek 忽略且出站不发送），并脱敏审计 `dropped_beta_headers`，绝不在响应里假装某 beta 生效。
5. nested JSON 字段在 ingress 禁止 decode 丢字段；provider 层 Drop 见 mapping 与内部审计。

## 数据库验收

1. `channels.protocol` 与 `channels.adapter_key` 已落库。
2. `providers.adapter` 已从正式 runtime schema、routing query、adapter registry、
   preflight 和 bootstrap seed 中移除；`channel.Runtime.AdapterKey` 只来自
   `channels.adapter_key`。
3. request、attempt、usage、price snapshot、cost snapshot 和 recovery schema 已升级。
4. migration 仍遵守一张表一组 up/down 文件。
5. query 仍遵守一张表一个文件。
6. 已执行本地库 down → 修改 migration → up 验证。
7. 已执行 `sqlc generate` 并检查旧生成物。

## 测试验收

1. OpenAI SDK 黑盒覆盖非流式、流式、reasoning、tools、response format、高级字段
   Drop/Pass/Adapt、usage 和错误；非目标能力字段验证 upstream body 不含 dropped 键。
2. Anthropic SDK 黑盒覆盖非流式、流式、system、thinking、tools、cache、Drop 字段、usage 和错误。
3. DeepSeek OpenAI mapping 与 DeepSeek Anthropic mapping 不存在状态列为 `Verify` 的残留；相关冻结由 DS-OAI / DS-ANT 黑盒用例覆盖。
4. DeepSeek 双协议 adapter 单元测试覆盖 request map、response map、stream 翻译、usage 和 error。
5. routing 测试覆盖同 provider 双协议 channel、registry capability 过滤与同协议 fallback。
6. settlement 测试覆盖 cache write、line item、write-off、risk exposure 和 recovery 幂等。
7. delivery 测试覆盖 completed、interrupted 和 not_started。
8. authorization 测试覆盖多个同协议 fallback candidate 的 tokenizer 保守估算，不能只按首选 channel 冻结。

自动化验证：

```bash
sqlc generate
go test ./internal/... ./cmd/...
go vet ./internal/... ./cmd/...
git diff --check
rg -n '^\| [^|]+ \| [^|]+ \| `Verify` \|' docs/chapters/phase-10-dual-protocol-gateway/DEEPSEEK_*_MAPPING.md
test ! -d internal/core/adapter/openai/streamtranslate
rg -n "providers\\.adapter" internal cmd migrations sql --glob '!internal/bootstrap/provider_adapter_preflight.go'
rg -n "adapter\\.ChatRequest|adapter\\.ChatResponse" internal cmd migrations sql
```

说明：仓库级 `go test ./...` 当前仍会被既有 `seed/` 目录双 `main` 阻断，与 Phase 10 无关；Phase 10 签核以 `./internal/... ./cmd/...` 为自动化范围。`adapter.ChatUsage` 在 Phase 10 关闭口径中允许作为 OpenAI 协议族内部响应解析、公开 usage DTO 映射与测试 helper，不允许作为账务事实输入。

关闭阶段前：

```text
rg -n '^\| [^|]+ \| [^|]+ \| `Verify` \|' docs/chapters/phase-10-dual-protocol-gateway/DEEPSEEK_*_MAPPING.md
```

必须无残留。

runtime adapter 绑定关闭检查：

```text
rg -n "providers\\.adapter" internal cmd migrations sql --glob '!internal/bootstrap/provider_adapter_preflight.go'
```

必须无残留。文档中只允许以历史背景或迁移说明形式提及。

## 文档验收

1. [ARCHITECTURE.md](ARCHITECTURE.md) 与代码目录一致。
2. [RESPONSE_FACTS.md](RESPONSE_FACTS.md) 与 schema、billing 和 recovery 一致。
3. 两个协议矩阵与公开 DTO 一致。
4. 两个 DeepSeek mapping 与 adapter、黑盒测试一致。
5. [docs/architecture/PROJECT_STRUCTURE.md](../../architecture/PROJECT_STRUCTURE.md) 已同步。
6. [docs/production/DECISIONS.md](../../production/DECISIONS.md) 已同步。
7. [docs/PROJECT_STATUS.md](../../PROJECT_STATUS.md) 已同步。
