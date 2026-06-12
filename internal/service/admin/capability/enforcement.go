package capability

import (
	"context"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/config"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

// EnforcementStore 是 observe 期判定分布的只读聚合能力。
type EnforcementStore interface {
	CountRequestsByCapabilityResult(ctx context.Context, arg sqlc.CountRequestsByCapabilityResultParams) ([]sqlc.CountRequestsByCapabilityResultRow, error)
}

// EnforcementMode 是单个 ingress 表面的 enforce 现状（只读，来源为 gateway 部署 env）。
type EnforcementMode struct {
	Surface   string
	Operation string
	EnvVar    string
	Enforced  bool
}

// ResultCount 是某 capability 闸门判定结论在区间内的请求计数；Result 为 nil 表示 NULL（bypassed）。
type ResultCount struct {
	Result *string
	Total  int64
}

// EnforcementService 提供 enforce 只读状态与 observe 期判定分布。
//
// enforce 开关现为 gateway 进程的 env（启动注入、运行不可改），admin 与 gateway 是不同进程：
// 这里展示的是 admin 自身进程读到的同名 env 快照，可能与 gateway 实际存在漂移（标注 deploy_env）。
// 真正翻 enforce 仍需改 gateway env + 重启。
type EnforcementService struct {
	store EnforcementStore
	modes []EnforcementMode
}

// NewEnforcementService 用 capability enforce 配置快照创建只读服务。
func NewEnforcementService(store EnforcementStore, cfg config.CapabilityConfig) *EnforcementService {
	modes := []EnforcementMode{
		{Surface: "openai_chat", Operation: "chat_completions", EnvVar: "CAPABILITY_ENFORCE_OPENAI_CHAT", Enforced: cfg.EnforceOpenAIChat},
		{Surface: "anthropic_messages", Operation: "messages", EnvVar: "CAPABILITY_ENFORCE_ANTHROPIC_MESSAGES", Enforced: cfg.EnforceAnthropicMessages},
		{Surface: "openai_responses", Operation: "responses", EnvVar: "CAPABILITY_ENFORCE_OPENAI_RESPONSES", Enforced: cfg.EnforceOpenAIResponses},
	}
	return &EnforcementService{store: store, modes: modes}
}

// Modes 返回三个 ingress 表面的 enforce 现状。
func (s *EnforcementService) Modes() []EnforcementMode {
	out := make([]EnforcementMode, len(s.modes))
	copy(out, s.modes)
	return out
}

// ObserveSummary 在可选时间范围内按 capability 闸门判定结论聚合请求计数。
func (s *EnforcementService) ObserveSummary(ctx context.Context, from, to *time.Time) ([]ResultCount, error) {
	rows, err := s.store.CountRequestsByCapabilityResult(ctx, sqlc.CountRequestsByCapabilityResultParams{
		FromTime: tsNarg(from),
		ToTime:   tsNarg(to),
	})
	if err != nil {
		return nil, storeFailed(err, "count requests by capability result")
	}

	out := make([]ResultCount, 0, len(rows))
	for _, row := range rows {
		rc := ResultCount{Total: row.Total}
		if row.CapabilityCheckResult.Valid {
			v := row.CapabilityCheckResult.String
			rc.Result = &v
		}
		out = append(out, rc)
	}
	return out, nil
}
