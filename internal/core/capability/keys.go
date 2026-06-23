// Package capability 承载能力架构的能力标识类型、支持级别与数据访问层。
//
// 能力 key 是公开稳定契约。合法 key 的真源是 DB 字典表 capability_keys（DEC-024 起，取代旧
// keys.go 常量注册表）；新增能力只需往字典表插一行（带中文描述），无需改代码。人类参考列表见
// docs/protocol/CAPABILITY_KEYS.md。本包负责 model_capabilities / 字典 / 同步任务的读写。
package capability

import "strings"

// Key 是稳定能力标识，命名形如 <domain>.<feature>[.<sub>]。
//
// 合法性以 DB 字典表 capability_keys 为准（写入 model_capabilities 受外键约束）；
// 代码内不再维护常量注册表，adapter 画像 / 目录粗能力等生产者直接用字符串字面量声明 key。
type Key string

// SupportLevel 表示某模型对某能力的支持级别。
type SupportLevel string

const (
	// SupportLevelFull 表示完整支持该能力。
	SupportLevelFull SupportLevel = "full"

	// SupportLevelLimited 表示部分支持，受 limits 进一步约束（仅作展示记录，DEC-024 后不参与运行时判定）。
	SupportLevelLimited SupportLevel = "limited"

	// SupportLevelUnsupported 表示不支持该能力。
	SupportLevelUnsupported SupportLevel = "unsupported"
)

// ProtocolScope 表示能力 key 的协议归属（capability_keys.protocol_scope）。
type ProtocolScope string

const (
	// ProtocolScopeShared 表示 OpenAI 与 Anthropic 双协议通用（Admin 展示「通用」）。
	ProtocolScopeShared ProtocolScope = "shared"

	// ProtocolScopeOpenAI 表示 OpenAI Chat Completions / Responses 专有。
	ProtocolScopeOpenAI ProtocolScope = "openai"

	// ProtocolScopeAnthropic 表示 Anthropic Messages API 专有。
	ProtocolScopeAnthropic ProtocolScope = "anthropic"
)

// IsValidProtocolScope 判断协议归属是否合法。
func IsValidProtocolScope(scope ProtocolScope) bool {
	switch scope {
	case ProtocolScopeShared, ProtocolScopeOpenAI, ProtocolScopeAnthropic:
		return true
	default:
		return false
	}
}

// NormalizeProtocolScope 把历史/缺省值归一为合法 protocol_scope（兼容旧 both）。
func NormalizeProtocolScope(raw string) ProtocolScope {
	switch ProtocolScope(strings.TrimSpace(raw)) {
	case ProtocolScopeOpenAI:
		return ProtocolScopeOpenAI
	case ProtocolScopeAnthropic:
		return ProtocolScopeAnthropic
	case ProtocolScopeShared, "both":
		return ProtocolScopeShared
	default:
		return ProtocolScopeShared
	}
}

// IsValidSupportLevel 判断支持级别是否是合法的模型层取值。
func IsValidSupportLevel(level SupportLevel) bool {
	switch level {
	case SupportLevelFull, SupportLevelLimited, SupportLevelUnsupported:
		return true
	default:
		return false
	}
}

// Source 表示同步任务来源（model_capability_sync_jobs.source 取值）。
//
// 阶段 14 起 model_capabilities 不再带 source（能力来源已无意义）；Source 仅保留给同步任务审计。
type Source string

const (
	// SourceModelsDev 表示来自 models.dev 同步。
	SourceModelsDev Source = "models_dev"

	// SourceManual 表示运营手工触发。
	SourceManual Source = "manual"
)

// IsValidSyncJobSource 判断同步任务来源是否合法（model_capability_sync_jobs.source 取值）。
func IsValidSyncJobSource(source Source) bool {
	switch source {
	case SourceModelsDev, SourceManual:
		return true
	default:
		return false
	}
}
