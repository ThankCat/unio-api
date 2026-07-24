package requestlog

import (
	"context"
	"time"
)

// RequestStatus 表示用户可见请求的生命周期状态。
type RequestStatus string

const (
	RequestStatusPending   RequestStatus = "pending"
	RequestStatusRunning   RequestStatus = "running"
	RequestStatusSucceeded RequestStatus = "succeeded"
	RequestStatusFailed    RequestStatus = "failed"
	RequestStatusCanceled  RequestStatus = "canceled"
)

// Protocol 表示公开 ingress 或上游调用使用的协议族。
type Protocol string

const (
	ProtocolOpenAI    Protocol = "openai"
	ProtocolAnthropic Protocol = "anthropic"
)

// Endpoint 表示公开 Gateway API 操作。
type Endpoint string

const (
	EndpointChatCompletions Endpoint = "chat_completions"
	EndpointMessages        Endpoint = "messages"
	EndpointResponses       Endpoint = "responses"
)

// UpstreamEndpoint identifies the concrete transport recorded by an attempt.
// It is separate from the public ingress Endpoint because one request can
// execute more than one upstream endpoint (for example Responses Compact).
type UpstreamEndpoint string

const (
	UpstreamEndpointChatCompletions  UpstreamEndpoint = "chat_completions"
	UpstreamEndpointResponses        UpstreamEndpoint = "responses"
	UpstreamEndpointResponsesCompact UpstreamEndpoint = "responses_compact"
	UpstreamEndpointMessages         UpstreamEndpoint = "messages"
)

// DeliveryStatus 表示客户响应交付状态，与 settlement 状态分开记录。
type DeliveryStatus string

const (
	DeliveryStatusNotStarted  DeliveryStatus = "not_started"
	DeliveryStatusInProgress  DeliveryStatus = "in_progress"
	DeliveryStatusCompleted   DeliveryStatus = "completed"
	DeliveryStatusInterrupted DeliveryStatus = "interrupted"
)

// AttemptStatus 表示一次上游 channel 尝试的生命周期状态。
type AttemptStatus string

const (
	AttemptStatusRunning   AttemptStatus = "running"
	AttemptStatusSucceeded AttemptStatus = "succeeded"
	AttemptStatusFailed    AttemptStatus = "failed"
	AttemptStatusCanceled  AttemptStatus = "canceled"
)

// CreateRequestParams 表示创建 request record 所需的请求事实。
type CreateRequestParams struct {
	RequestID        string
	UserID           int64
	APIKeyID         int64
	RequestedModelID string
	IngressProtocol  Protocol
	Endpoint        Endpoint
	Stream           bool
	StartedAt        time.Time
	// 批二富化（均可空）：线路快照 / 归一推理强度 + 原始预算 / 客户端 IP。
	RouteID               *int64
	ReasoningEffort       *string
	ReasoningBudgetTokens *int32
	ClientIP              *string
}

// RequestRecord 表示一次用户可见请求记录。
type RequestRecord struct {
	ID                  int64
	RequestID           string
	UserID              int64
	APIKeyID            int64
	RequestedModelID    string
	IngressProtocol     Protocol
	Endpoint           Endpoint
	ResponseModelID     *string
	ResponseProtocol    *string
	ResponseID          *string
	Stream              bool
	Status              RequestStatus
	FinalProviderID     *int64
	FinalChannelID      *int64
	ErrorCode           *string
	ErrorMessage        *string
	InternalErrorDetail *string
	DeliveryStatus      DeliveryStatus
	ResponseStartedAt   *time.Time
	ResponseCompletedAt *time.Time
	StartedAt           time.Time
	CompletedAt         *time.Time
}

// MarkRequestSucceededParams 表示标记请求成功所需的最终事实。
// response_completed_at 不在此处写入：它归属交付状态机（delivery_status='completed' 时落地），
// 结算阶段交付尚未完成，强写会违反 ck_request_records_delivery_completed_at。
type MarkRequestSucceededParams struct {
	ID                int64
	ResponseModelID   string
	ResponseProtocol  Protocol
	ResponseID        string
	FinalProviderID   int64
	FinalChannelID    int64
	ResponseStartedAt *time.Time
	CompletedAt       time.Time
}

// MarkSettledRequestCanceledParams 表示已结算但客户取消的请求终态事实。
type MarkSettledRequestCanceledParams struct {
	MarkRequestSucceededParams
	ErrorCode           string
	ErrorMessage        string
	InternalErrorDetail string
}

// MarkSettledRequestFailedParams 表示已结算但上游失败的请求终态事实。
type MarkSettledRequestFailedParams struct {
	MarkRequestSucceededParams
	ErrorCode           string
	ErrorMessage        string
	InternalErrorDetail string
}

// MarkResponseStartedParams 表示记录首次客户可见响应时间所需的事实。
type MarkResponseStartedParams struct {
	ID                int64
	ResponseStartedAt time.Time
}

// MarkRequestFailedParams 表示标记请求失败所需的错误事实。
type MarkRequestFailedParams struct {
	ID                  int64
	ErrorCode           string
	ErrorMessage        string
	InternalErrorDetail string
	CompletedAt         time.Time
}

// MarkRequestCanceledParams 表示标记请求取消所需的错误事实。
type MarkRequestCanceledParams struct {
	ID                  int64
	ErrorCode           string
	ErrorMessage        string
	InternalErrorDetail string
	CompletedAt         time.Time
}

// CreateAttemptParams 表示创建 request attempt 所需的上游尝试事实。
type CreateAttemptParams struct {
	RequestRecordID                 int64
	AttemptIndex                    int
	ProviderID                      int64
	ChannelID                       int64
	AdapterKey                      string
	UpstreamModel                   string
	UpstreamProtocol                Protocol
	ProviderOriginID              *int64
	ProviderOriginBaseURLRevision *int64
	ProviderOriginStatusRevision  *int64
	ChannelConfigRevision           *int64
	RoutingCandidateIndex           *int
	UpstreamEndpoint               UpstreamEndpoint
	StartedAt                       time.Time
}

// AttemptRecord 表示一次上游 channel 尝试记录。
type AttemptRecord struct {
	ID                              int64
	RequestRecordID                 int64
	AttemptIndex                    int
	ProviderID                      int64
	ChannelID                       int64
	AdapterKey                      string
	UpstreamModel                   string
	UpstreamProtocol                Protocol
	ProviderOriginID              *int64
	ProviderOriginBaseURLRevision *int64
	ProviderOriginStatusRevision  *int64
	ChannelConfigRevision           *int64
	RoutingCandidateIndex           *int
	UpstreamEndpoint               UpstreamEndpoint
	UpstreamResponseID              *string
	UpstreamResponseModel           *string
	UpstreamFinishReason            *string
	FinishClass                     *string
	Status                          AttemptStatus
	UpstreamStatusCode              *int
	UpstreamRequestID               *string
	ErrorCode                       *string
	ErrorMessage                    *string
	InternalErrorDetail             *string
	ResponseStartedAt               *time.Time
	UpstreamStartedAt               *time.Time
	UpstreamFirstTokenAt            *time.Time
	UpstreamCompletedAt             *time.Time
	BreakerOriginDisposition      *string
	BreakerChannelDisposition       *string
	FinalUsageReceived              bool
	UsageMappingVersion             *string
	StartedAt                       time.Time
	CompletedAt                     *time.Time
}

// MarkAttemptSucceededParams 表示标记上游尝试成功所需的最终事实。
type MarkAttemptSucceededParams struct {
	ID                    int64
	UpstreamResponseID    string
	UpstreamResponseModel string
	UpstreamFinishReason  string
	FinishClass           string
	UpstreamStatusCode    int
	UpstreamRequestID     *string
	ResponseStartedAt     *time.Time
	// FinalUsageReceived 标记本次成功是否基于上游真实 final usage。
	// full bill 传 true；partial settlement（合成估算事实）传 false，作为渠道未回 usage 的审计信号。
	FinalUsageReceived  bool
	UsageMappingVersion string
	CompletedAt         time.Time
}

// MarkSettledAttemptCanceledParams 表示已结算但客户取消的 attempt 终态事实。
type MarkSettledAttemptCanceledParams struct {
	MarkAttemptSucceededParams
	ErrorCode           string
	ErrorMessage        string
	InternalErrorDetail string
}

// MarkSettledAttemptFailedParams 表示已结算但上游失败的 attempt 终态事实。
type MarkSettledAttemptFailedParams struct {
	MarkAttemptSucceededParams
	ErrorCode           string
	ErrorMessage        string
	InternalErrorDetail string
}

// MarkAttemptResponseStartedParams 表示记录一次 attempt 首次客户可见响应时间所需的事实。
type MarkAttemptResponseStartedParams struct {
	ID                int64
	ResponseStartedAt time.Time
}

// RecordAttemptTimingParams carries first-write-wins upstream transport facts.
// Nil values mean that lifecycle has not observed that boundary yet.
type RecordAttemptTimingParams struct {
	ID                   int64
	UpstreamStartedAt    *time.Time
	UpstreamFirstTokenAt *time.Time
	UpstreamCompletedAt  *time.Time
}

// RecordAttemptBreakerDispositionParams first-write-wins 地保存 AttemptPermit Finish 的双作用域结果。
type RecordAttemptBreakerDispositionParams struct {
	ID                  int64
	OriginDisposition string
	ChannelDisposition  string
}

// MarkAttemptFailedParams 表示标记上游尝试失败所需的错误事实。
type MarkAttemptFailedParams struct {
	ID                  int64
	UpstreamStatusCode  *int
	UpstreamRequestID   *string
	ErrorCode           string
	ErrorMessage        string
	InternalErrorDetail string
	CompletedAt         time.Time
}

// MarkAttemptCanceledParams 表示标记上游尝试取消所需的错误事实。
type MarkAttemptCanceledParams struct {
	ID                  int64
	ErrorCode           string
	ErrorMessage        string
	InternalErrorDetail string
	CompletedAt         time.Time
}

// Service 定义 request log 写入能力。
// 它只负责请求与上游尝试的审计状态，不负责 usage、price snapshot 或 ledger 扣费。
type Service interface {
	CreateRequest(ctx context.Context, params CreateRequestParams) (RequestRecord, error)
	MarkRequestRunning(ctx context.Context, id int64) (RequestRecord, error)
	MarkRequestResponseStarted(ctx context.Context, params MarkResponseStartedParams) (RequestRecord, error)
	MarkRequestDeliveryCompleted(ctx context.Context, id int64, completedAt time.Time) (RequestRecord, error)
	MarkRequestDeliveryInterrupted(ctx context.Context, id int64) (RequestRecord, error)
	MarkRequestSucceeded(ctx context.Context, params MarkRequestSucceededParams) (RequestRecord, error)
	MarkSettledRequestFailed(ctx context.Context, params MarkSettledRequestFailedParams) (RequestRecord, error)
	MarkSettledRequestCanceled(ctx context.Context, params MarkSettledRequestCanceledParams) (RequestRecord, error)
	MarkRequestFailed(ctx context.Context, params MarkRequestFailedParams) (RequestRecord, error)
	MarkRequestCanceled(ctx context.Context, params MarkRequestCanceledParams) (RequestRecord, error)

	CreateAttempt(ctx context.Context, params CreateAttemptParams) (AttemptRecord, error)
	MarkAttemptResponseStarted(ctx context.Context, params MarkAttemptResponseStartedParams) (AttemptRecord, error)
	MarkAttemptSucceeded(ctx context.Context, params MarkAttemptSucceededParams) (AttemptRecord, error)
	MarkSettledAttemptFailed(ctx context.Context, params MarkSettledAttemptFailedParams) (AttemptRecord, error)
	MarkSettledAttemptCanceled(ctx context.Context, params MarkSettledAttemptCanceledParams) (AttemptRecord, error)
	MarkAttemptFailed(ctx context.Context, params MarkAttemptFailedParams) (AttemptRecord, error)
	MarkAttemptCanceled(ctx context.Context, params MarkAttemptCanceledParams) (AttemptRecord, error)
}
