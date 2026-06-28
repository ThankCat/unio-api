package responses

import "encoding/json"

// ResponsesRequest 表示 OpenAI Responses API (POST /v1/responses) 请求体。
//
// 本 DTO 只负责协议结构 decode 与校验；字段语义到内部 openai.ChatRequest 的映射在
// responses_chat_map（TASK-11.05），结构性策略矩阵见 RESPONSES_CHAT_BRIDGE.md §1。
type ResponsesRequest struct {
	// Model 是客户模型名；按方案 A（DEC-014）等于 Unio 模型目录 model_id，复用既有 routing。
	Model string `json:"model"`

	// Input 是 Responses 输入：单条字符串或 input item 数组（见 ResponsesInput）。
	Input ResponsesInput `json:"input"`

	// Instructions 是系统级指令；桥接为首条 system message。
	Instructions *string `json:"instructions,omitempty"`

	// MaxOutputTokens 是最大输出 token；桥接为 Chat max_tokens / max_completion_tokens。
	MaxOutputTokens *ResponsesInt `json:"max_output_tokens,omitempty"`

	// 采样温度与核采样，原样映射到 Chat。
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`

	// Stream 是否流式；Codex 恒 true。
	Stream *bool `json:"stream,omitempty"`

	// Store 是否服务端存储输出；Unio 第一版无状态，adapter 出站 Drop（GAP-11-001）。
	Store *bool `json:"store,omitempty"`

	// ParallelToolCalls 是否允许并行工具调用。
	ParallelToolCalls *bool `json:"parallel_tool_calls,omitempty"`

	// Tools / ToolChoice 是 Responses 工具定义与选择策略（见 tools.go）。
	Tools      []ResponsesTool `json:"tools,omitempty"`
	ToolChoice json.RawMessage `json:"tool_choice,omitempty"`

	// Reasoning 是 reasoning 配置（effort/summary）；Codex 非 reasoning run 时显式为 null（→ nil）。
	Reasoning *ResponsesReasoning `json:"reasoning,omitempty"`

	// Text 是输出文本控制（format/verbosity）。
	Text *ResponsesTextControls `json:"text,omitempty"`

	// Include 是额外输出项控制；Responses 专属，契约无承载字段，translation Drop（GAP-11-004）。
	Include []string `json:"include,omitempty"`

	// Metadata 是公开协议 metadata（不等于内部 observability metadata）；保留原始 JSON。
	Metadata json.RawMessage `json:"metadata,omitempty"`

	// User / SafetyIdentifier 是终端用户标识；SafetyIdentifier 不等于 user。
	User             *string `json:"user,omitempty"`
	SafetyIdentifier *string `json:"safety_identifier,omitempty"`

	// PreviousResponseID 是会话续接键；无状态第一版 Drop 或 Reject（GAP-11-001）。
	PreviousResponseID *string `json:"previous_response_id,omitempty"`

	// Truncation 是截断策略；无服务端上下文管理，translation Drop。
	Truncation *string `json:"truncation,omitempty"`

	// ServiceTier 是服务等级；adapter 出站按能力 Drop。
	ServiceTier *string `json:"service_tier,omitempty"`

	// PromptCacheKey / PromptCacheRetention 是 prompt cache 路由键。
	PromptCacheKey       *string `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention *string `json:"prompt_cache_retention,omitempty"`

	// Background 是异步任务模式；不支持，translation Drop/Reject（GAP-11-004）。
	Background *bool `json:"background,omitempty"`

	// Extensions 保留未显式建模的合法顶层字段（如 Codex 专属 client_metadata）；
	// 由 UnmarshalJSON 填充。DEC-012：decode 不丢字段。
	Extensions map[string]json.RawMessage `json:"-"`

	// raw 是 decode 时保留的原始请求体（DEC-012：上游 responses 直传据此零损耗重放，
	// 仅由 service 改写 model/stream 两个字段）。由 UnmarshalJSON 填充，经 RawBody 读取。
	raw json.RawMessage
}

// RawBody 返回 decode 时保留的原始请求体；未经 UnmarshalJSON 构造（如单测直接建结构体）时为 nil。
//
// 上游 responses 直传用它作为发往上游的请求基底，零损耗保留客户原始字段（含显式 null 与未建模扩展）。
func (req *ResponsesRequest) RawBody() json.RawMessage {
	if req == nil {
		return nil
	}
	return req.raw
}

// StreamEnabled 判断客户端是否请求流式响应。
func (req *ResponsesRequest) StreamEnabled() bool {
	return req.Stream != nil && *req.Stream
}

// HasExtension 判断请求是否保留了指定扩展字段。
func (req *ResponsesRequest) HasExtension(name string) bool {
	_, ok := req.Extensions[name]
	return ok
}

// Extension 返回指定扩展字段的原始 JSON；不存在时返回 nil。
func (req *ResponsesRequest) Extension(name string) json.RawMessage {
	if req == nil || req.Extensions == nil {
		return nil
	}
	return req.Extensions[name]
}

// ResponsesInput 表示 Responses `input` union：单条字符串或 input item 数组。
type ResponsesInput struct {
	// Raw 是 input 的原始 JSON，供精确报错与审计使用。
	Raw json.RawMessage
	// Text 为 input 是字符串时的值。
	Text *string
	// Items 为 input 是 item 数组时的值。
	Items []ResponseInputItem
}

// ResponsesInt 承载 Responses 文档里标为 number、但语义为整数计数的字段。
//
// OpenAI 文档把 max_output_tokens 写作 number；入口接受 256 与 256.0，
// 但保留 256.5 到 validation 阶段给出字段级错误，避免 decode 阶段泛化成 invalid json body。
type ResponsesInt struct {
	value    int
	integral bool
}

// Int 返回普通 int 值，供内部 adapter 使用。
func (n ResponsesInt) Int() int {
	return n.value
}

// Integral 表示原始 JSON number 是否可无损表达为整数。
func (n ResponsesInt) Integral() bool {
	return n.integral
}

// MarshalJSON 按数字输出，保持 Responses 响应回显形状。
func (n ResponsesInt) MarshalJSON() ([]byte, error) {
	return json.Marshal(n.value)
}

// MarshalJSON 把 input 还原为其原始 JSON（保留字符串/数组两种 union 形态）。
//
// 仅用于「无原始请求体」的 typed 重编码兜底路径（上游 responses 直传优先用 ResponsesRequest.RawBody）；
// ingress 入站只 decode、不 encode，本方法不影响既有路径。
func (in ResponsesInput) MarshalJSON() ([]byte, error) {
	if len(in.Raw) > 0 {
		return in.Raw, nil
	}
	return []byte("null"), nil
}

// ResponseInputItem 表示 Responses `input[]` 中的单个 item（按 type 区分的 union）。
//
// 只建模桥接需要消费/识别的字段；message 与 reasoning 共用 Content。详见 RESPONSES_CHAT_BRIDGE.md §2。
type ResponseInputItem struct {
	// Type 是 item 判别字段（message/function_call/function_call_output/reasoning/item_reference/...）。
	// 缺省时按 OpenAI 语义视为 message。
	Type string `json:"type,omitempty"`

	// message / reasoning: role + content（string | content parts[]）。
	Role    string          `json:"role,omitempty"`
	Content json.RawMessage `json:"content,omitempty"`

	// function_call: call_id / name / arguments；MCP namespace 工具额外带 namespace。
	// arguments 在 function_call 上通常是 JSON 字符串，但 Responses 也有未知/future item
	// 使用 arguments: unknown；保留原始 JSON，避免入口 decode 过早拒绝合法协议扩展。
	CallID    *string         `json:"call_id,omitempty"`
	Name      *string         `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Namespace *string         `json:"namespace,omitempty"`

	// function_call_output: call_id + output（string | parts[]）。
	Output json.RawMessage `json:"output,omitempty"`

	// reasoning / item_reference / compaction: id；reasoning 另有 summary[] 与 encrypted_content。
	ID               *string         `json:"id,omitempty"`
	Summary          json.RawMessage `json:"summary,omitempty"`
	EncryptedContent *string         `json:"encrypted_content,omitempty"`

	// Status 是 item 状态（多见于回传的历史 output item）。
	Status *string `json:"status,omitempty"`
}

// ResponsesReasoning 表示 Responses `reasoning` 配置对象。
type ResponsesReasoning struct {
	// Effort 是推理强度（如 low/medium/high/none）；桥接为 Chat reasoning_effort。
	Effort *string `json:"effort,omitempty"`
	// Summary 控制 reasoning summary（如 auto/concise/detailed）；影响是否发 reasoning 事件。
	Summary *string `json:"summary,omitempty"`
}

// ResponsesTextControls 表示 Responses `text` 输出控制对象。
type ResponsesTextControls struct {
	// Format 是输出格式（{type:"text"|"json_object"|"json_schema",...}）；桥接为 Chat response_format。
	Format json.RawMessage `json:"format,omitempty"`
	// Verbosity 是输出详细度（low/medium/high）；桥接为 Chat verbosity。
	Verbosity *string `json:"verbosity,omitempty"`
}

// ResponsesResponse 表示 Responses 非流式响应对象，也用作流式 created/completed 事件中的 response。
type ResponsesResponse struct {
	ID                string                      `json:"id"`
	Object            string                      `json:"object"`
	CreatedAt         int64                       `json:"created_at"`
	Model             string                      `json:"model"`
	Status            string                      `json:"status"`
	Output            []ResponseOutputItem        `json:"output"`
	Usage             *ResponsesUsage             `json:"usage,omitempty"`
	IncompleteDetails *ResponsesIncompleteDetails `json:"incomplete_details,omitempty"`
	Error             *ResponsesErrorObject       `json:"error,omitempty"`

	// 以下为请求参数回显（best-effort）；上游/请求未提供时省略。
	ParallelToolCalls *bool    `json:"parallel_tool_calls,omitempty"`
	Temperature       *float64 `json:"temperature,omitempty"`
	TopP              *float64 `json:"top_p,omitempty"`
	MaxOutputTokens   *int     `json:"max_output_tokens,omitempty"`

	// raw 非空时 MarshalJSON 直接返回它：上游 responses 直传零转换透传原始响应体
	// （service 已预先改写顶层 model 回显）。桥接路径不设置本字段，按 typed 字段正常 marshal。
	raw json.RawMessage
}

// RawResponsesResponse 构造一个「原文直传」响应：MarshalJSON 将原样返回 raw（不经 typed 字段重编码）。
func RawResponsesResponse(raw json.RawMessage) *ResponsesResponse {
	return &ResponsesResponse{raw: raw}
}

// MarshalJSON 在 raw 非空时原样透传上游响应体；否则按 typed 字段正常序列化（桥接路径）。
func (r ResponsesResponse) MarshalJSON() ([]byte, error) {
	if len(r.raw) > 0 {
		return r.raw, nil
	}
	type alias ResponsesResponse
	return json.Marshal(alias(r))
}

// ResponseOutputItem 表示 Responses output[] 中的单个 item（message/reasoning/function_call）。
type ResponseOutputItem struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`

	// message: role + status + content（output_text/refusal parts）。
	Role    string                  `json:"role,omitempty"`
	Status  string                  `json:"status,omitempty"`
	Content []ResponseOutputContent `json:"content,omitempty"`

	// reasoning: summary（summary_text parts）+ content（reasoning_text parts，复用 Content）
	// + encrypted_content（DeepSeek 省略）。
	Summary          []ResponseOutputContent `json:"summary,omitempty"`
	EncryptedContent *string                 `json:"encrypted_content,omitempty"`

	// function_call: call_id / name / arguments；MCP namespace 工具回填 namespace。
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

// MarshalJSON 对 message item 始终输出 content 字段（空时为 []）。
//
// Codex（responses 解析）的 ResponseItem::Message 把 content 当作必填，缺省会导致 output_item.added
// 反序列化失败、item 不被登记，后续 output_text.delta 报 "OutputTextDelta without active item" 并丢字。
// 仅对 message 强制；reasoning/function_call 仍按 omitempty（与上游 OpenAI 形状一致）。
func (i ResponseOutputItem) MarshalJSON() ([]byte, error) {
	type alias ResponseOutputItem
	if i.Type == "message" {
		content := i.Content
		if content == nil {
			content = []ResponseOutputContent{}
		}
		return json.Marshal(struct {
			alias
			Content []ResponseOutputContent `json:"content"`
		}{alias(i), content})
	}
	return json.Marshal(alias(i))
}

// ResponseOutputContent 表示 output / reasoning item content 或 summary 中的单个 part。
//
// Type 取值：output_text / refusal（message content）；reasoning_text（reasoning content）；
// summary_text（reasoning summary）。
type ResponseOutputContent struct {
	Type        string            `json:"type"`
	Text        string            `json:"text,omitempty"`
	Refusal     string            `json:"refusal,omitempty"`
	Annotations []json.RawMessage `json:"annotations,omitempty"`
}

// ResponsesUsage 表示 Responses usage（由 Chat usage 映射，仅供客户/SDK 读取，不作账务事实）。
type ResponsesUsage struct {
	InputTokens         int                           `json:"input_tokens"`
	OutputTokens        int                           `json:"output_tokens"`
	TotalTokens         int                           `json:"total_tokens"`
	InputTokensDetails  *ResponsesInputTokensDetails  `json:"input_tokens_details,omitempty"`
	OutputTokensDetails *ResponsesOutputTokensDetails `json:"output_tokens_details,omitempty"`
}

// ResponsesInputTokensDetails 是 Responses input_tokens_details。
type ResponsesInputTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

// ResponsesOutputTokensDetails 是 Responses output_tokens_details。
type ResponsesOutputTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// ResponsesIncompleteDetails 是 status=incomplete 时的原因对象。
type ResponsesIncompleteDetails struct {
	Reason string `json:"reason"`
}

// ResponsesErrorObject 是 Responses 原生 error 对象（{type,code,message,param}）。
type ResponsesErrorObject struct {
	Type    string  `json:"type"`
	Code    string  `json:"code,omitempty"`
	Message string  `json:"message"`
	Param   *string `json:"param,omitempty"`
}

// InputTokenCountResponse 是 POST /v1/responses/input_tokens 的响应体。
//
// 形状由 openai-* SDK 类型确认：两字段必填，Object 恒为 "response.input_tokens"。
type InputTokenCountResponse struct {
	InputTokens int    `json:"input_tokens"`
	Object      string `json:"object"`
}

// CompactHistoryResponse 是 POST /v1/responses/compact 的响应体。
//
// 形状由 codex-rs codex-api/src/endpoint/compact.rs 确认：{"output":[ResponseItem,...]}，
// 非完整 response 对象、非 SSE。
type CompactHistoryResponse struct {
	Output []ResponseOutputItem `json:"output"`

	// raw 非空时 MarshalJSON 原样返回它：NativeCompact 原文透传上游 /responses/compact 响应体
	// （service 已预先改写顶层 model 回显）。SyntheticCompact 不设置本字段，按 typed Output 正常 marshal。
	raw json.RawMessage
}

// RawCompactHistoryResponse 构造一个「原文直传」compact 响应：MarshalJSON 原样返回 raw（NativeCompact）。
func RawCompactHistoryResponse(raw json.RawMessage) *CompactHistoryResponse {
	return &CompactHistoryResponse{raw: raw}
}

// MarshalJSON 在 raw 非空时原样透传上游压缩响应体；否则按 typed Output 正常序列化（SyntheticCompact）。
func (r CompactHistoryResponse) MarshalJSON() ([]byte, error) {
	if len(r.raw) > 0 {
		return r.raw, nil
	}
	type alias CompactHistoryResponse
	return json.Marshal(alias(r))
}
