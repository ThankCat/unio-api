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

// Operation 表示公开 Gateway API 操作。
type Operation string

const (
	OperationChatCompletions Operation = "chat_completions"
	OperationMessages        Operation = "messages"
	OperationResponses       Operation = "responses"
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
	ProjectID        int64
	APIKeyID         int64
	RequestedModelID string
	IngressProtocol  Protocol
	Operation        Operation
	Stream           bool
	StartedAt        time.Time
}

// RequestRecord 表示一次用户可见请求记录。
type RequestRecord struct {
	ID                  int64
	RequestID           string
	UserID              int64
	ProjectID           int64
	APIKeyID            int64
	RequestedModelID    string
	IngressProtocol     Protocol
	Operation           Operation
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
type MarkRequestSucceededParams struct {
	ID               int64
	ResponseModelID  string
	ResponseProtocol Protocol
	ResponseID       string
	FinalProviderID  int64
	FinalChannelID   int64
	CompletedAt      time.Time
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
	RequestRecordID  int64
	AttemptIndex     int
	ProviderID       int64
	ChannelID        int64
	AdapterKey       string
	UpstreamModel    string
	UpstreamProtocol Protocol
	// RequiredCapabilities 是 ingress 推断的本次请求所需能力 key（capability 闸门 observe 审计快照，可空）。
	RequiredCapabilities []string
	StartedAt            time.Time
}

// AttemptRecord 表示一次上游 channel 尝试记录。
type AttemptRecord struct {
	ID                    int64
	RequestRecordID       int64
	AttemptIndex          int
	ProviderID            int64
	ChannelID             int64
	AdapterKey            string
	UpstreamModel         string
	UpstreamProtocol      Protocol
	UpstreamResponseID    *string
	UpstreamResponseModel *string
	UpstreamFinishReason  *string
	FinishClass           *string
	Status                AttemptStatus
	UpstreamStatusCode    *int
	UpstreamRequestID     *string
	ErrorCode             *string
	ErrorMessage          *string
	InternalErrorDetail   *string
	ResponseStartedAt     *time.Time
	FinalUsageReceived    bool
	UsageMappingVersion   *string
	RequiredCapabilities  []string
	StartedAt             time.Time
	CompletedAt           *time.Time
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
	UsageMappingVersion   string
	CompletedAt           time.Time
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
	// SetCapabilityCheckResult 写入 capability 闸门判定结论审计（阶段 12 observe），与状态机解耦。
	SetCapabilityCheckResult(ctx context.Context, id int64, result string) error
	MarkRequestSucceeded(ctx context.Context, params MarkRequestSucceededParams) (RequestRecord, error)
	MarkRequestFailed(ctx context.Context, params MarkRequestFailedParams) (RequestRecord, error)
	MarkRequestCanceled(ctx context.Context, params MarkRequestCanceledParams) (RequestRecord, error)

	CreateAttempt(ctx context.Context, params CreateAttemptParams) (AttemptRecord, error)
	MarkAttemptSucceeded(ctx context.Context, params MarkAttemptSucceededParams) (AttemptRecord, error)
	MarkAttemptFailed(ctx context.Context, params MarkAttemptFailedParams) (AttemptRecord, error)
	MarkAttemptCanceled(ctx context.Context, params MarkAttemptCanceledParams) (AttemptRecord, error)
}
