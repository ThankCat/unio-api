package lifecycle

import (
	"sync"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
)

// CredentialRevision pins one upstream result to the exact routing/credential generation used by
// the real transport. A late 401 may invalidate only while all three revisions are still current.
type CredentialRevision struct {
	ChannelID               int64
	ChannelConfigRevision   int64
	EndpointBaseURLRevision int64
	EndpointStatusRevision  int64
}

// CredentialInvalidator 在渠道被判定「凭据失效」时执行持久化副作用：把 channels.credential_valid
// 翻为 false，并在真跳变时追加一条 runtime_401 事件日志。由 bootstrap 用 sqlc 存储实现并注入。
//
// 实现必须自行异步、best-effort（不阻塞请求热路径，不因 DB 抖动影响在途请求）。nil 表示不启用持久闸门。
type CredentialInvalidator interface {
	MarkChannelCredentialInvalid(CredentialRevision)
}

// CredentialGate 记录每渠道「连续 401」次数，达到阈值触发一次持久失效翻牌（凭据闸门 B 层）。
//
// 与 Redis 全局 breaker（瞬时错误率）正交：401 归此闸门专管，达到阈值后由 CredentialInvalidator 持久摘除，
// 后续请求在路由候选层（credential_valid）直接跳过该渠道，直到检测通过才恢复。
type CredentialGate interface {
	// RecordResult 消费一次上游尝试结果：成功→清零；401→累加（到阈值翻失效并清零）；其它错误→不改计数。
	RecordResult(CredentialRevision, error)
}

// ChannelCredentialGate 是按 channel 维度的进程内「连续 401」计数器。
//
// 设计取舍与 ChannelCircuitBreaker 一致：进程内状态、每实例独立。多实例下第一个数到阈值的实例
// 翻 DB flag，其余实例随后从 DB（路由候选）看到失效即停选，无需共享存储。
// threshold 可运行时热改（SetThreshold），由 mu 保护。
type ChannelCredentialGate struct {
	invalidator CredentialInvalidator

	mu        sync.Mutex
	threshold int
	count     map[CredentialRevision]int
}

// NewChannelCredentialGate 创建凭据闸门。threshold<=0 兜底为 3（连续 3 次 401 翻失效）。
func NewChannelCredentialGate(threshold int, invalidator CredentialInvalidator) *ChannelCredentialGate {
	if threshold <= 0 {
		threshold = 3
	}
	return &ChannelCredentialGate{
		threshold:   threshold,
		invalidator: invalidator,
		count:       make(map[CredentialRevision]int),
	}
}

// SetThreshold 原子替换 401 阈值（运行时热改入口）；<=0 沿用构造相同的兜底 3。
// 各渠道进行中的连续计数保留，下次 401 判定即用新阈值。
func (g *ChannelCredentialGate) SetThreshold(threshold int) {
	if g == nil {
		return
	}
	if threshold <= 0 {
		threshold = 3
	}
	g.mu.Lock()
	g.threshold = threshold
	g.mu.Unlock()
}

// RecordResult 实现 CredentialGate。
func (g *ChannelCredentialGate) RecordResult(revision CredentialRevision, err error) {
	if g == nil {
		return
	}
	if revision.ChannelID <= 0 || revision.ChannelConfigRevision <= 0 ||
		revision.EndpointBaseURLRevision <= 0 || revision.EndpointStatusRevision <= 0 {
		return
	}

	if err == nil {
		// 成功打断连续 401，清零（C-2）。
		g.mu.Lock()
		delete(g.count, revision)
		g.mu.Unlock()
		return
	}

	category, ok := adapter.UpstreamCategoryOf(err)
	if !ok || category != adapter.UpstreamErrorAuth {
		// 非 401 失败（超时/5xx/429/bad_request/取消/未分类）：不 +1 也不清零（C-2）。
		return
	}

	g.mu.Lock()
	g.count[revision]++
	reached := g.count[revision] >= g.threshold
	if reached {
		delete(g.count, revision)
	}
	g.mu.Unlock()

	if reached && g.invalidator != nil {
		g.invalidator.MarkChannelCredentialInvalid(revision)
	}
}
