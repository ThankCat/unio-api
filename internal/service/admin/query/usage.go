package query

import (
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// Usage 是请求详情中的协议无关用量事实（token 各桶 + 来源/映射版本）。
// 注：独立的「用量分析」列表页已下线（用量并入请求记录），此结构仅供请求详情复用。
type Usage struct {
	ID                       int64
	RequestRecordID          int64
	UncachedInputTokens      int64
	CacheReadInputTokens     int64
	CacheWrite5mInputTokens  int64
	CacheWrite1hInputTokens  int64
	CacheWrite30mInputTokens int64
	OutputTokensTotal        int64
	ReasoningOutputTokens    int64
	UsageSource              string
	UsageMappingVersion      string
	CreatedAt                time.Time
}

func toUsage(u sqlc.UsageRecord) Usage {
	return Usage{
		ID:                       u.ID,
		RequestRecordID:          u.RequestRecordID,
		UncachedInputTokens:      u.UncachedInputTokens,
		CacheReadInputTokens:     u.CacheReadInputTokens,
		CacheWrite5mInputTokens:  u.CacheWrite5mInputTokens,
		CacheWrite1hInputTokens:  u.CacheWrite1hInputTokens,
		CacheWrite30mInputTokens: u.CacheWrite30mInputTokens,
		OutputTokensTotal:        u.OutputTokensTotal,
		ReasoningOutputTokens:    u.ReasoningOutputTokens,
		UsageSource:              u.UsageSource,
		UsageMappingVersion:      u.UsageMappingVersion,
		CreatedAt:                u.CreatedAt.Time,
	}
}
