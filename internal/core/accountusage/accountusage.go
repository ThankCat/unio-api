// Package accountusage 表达池型账号「用量水位 vs 暂停阈值」的纯判定：
//
//   - 阈值按「账号 → 池型渠道 → 全局 setting」三层继承：账号与渠道两层 NULL 继承上一层、1~100 覆写，
//     不接受 0（「基本不拦」= 设 100，只在完全打满时暂停）；
//   - 判定输入是账号最近一次用量快照（subscription_accounts.usage_snapshot）与生效阈值，
//     输出「此刻是否应移出调度、到哪个时刻恢复」。
//
// 候选准备（每请求）、用量观测（Recorder）与阈值变更后的运行态重算（Reconciler）共用同一份规则，
// 任一层阈值改动才能对下一次请求立即生效，而不依赖 Redis 标记被谁刷新过。本包是纯函数，不做 IO。
package accountusage

import (
	"encoding/json"
	"time"
)

// 阈值合法值域（百分比整数，闭区间）。
const (
	MinThresholdPercent int32 = 1
	MaxThresholdPercent int32 = 100
)

// DefaultThresholdPercent 是全局阈值的代码默认（appsettings 未配置时）：还剩 10% 时停止派新请求，
// 给在途请求与换号轮转留缓冲。
const DefaultThresholdPercent int32 = 90

// ValidThreshold 判断一个显式阈值是否落在 1~100。
func ValidThreshold(percent int32) bool {
	return percent >= MinThresholdPercent && percent <= MaxThresholdPercent
}

// ThresholdSource 标出生效阈值来自哪一层，供管理端展示「继承自渠道 / 全局」。
type ThresholdSource string

const (
	ThresholdSourceAccount ThresholdSource = "account"
	ThresholdSourceChannel ThresholdSource = "channel"
	ThresholdSourceGlobal  ThresholdSource = "global"
)

// Threshold 是解析完成的生效阈值。
type Threshold struct {
	Percent int32
	Source  ThresholdSource
}

// ResolveThreshold 执行三层继承：账号显式值 → 渠道显式值 → 全局。
// 非法的显式值（<1 或 >100，理论上被 DB CHECK 拦住）按 NULL 处理继续向上继承，
// 全局值非法时回落 100（只在完全打满时暂停，宁可多放一个请求也不把整池锁死）。
func ResolveThreshold(account, channel *int32, global int32) Threshold {
	if account != nil && ValidThreshold(*account) {
		return Threshold{Percent: *account, Source: ThresholdSourceAccount}
	}
	if channel != nil && ValidThreshold(*channel) {
		return Threshold{Percent: *channel, Source: ThresholdSourceChannel}
	}
	if !ValidThreshold(global) {
		global = MaxThresholdPercent
	}
	return Threshold{Percent: global, Source: ThresholdSourceGlobal}
}

// WindowName 标出触顶的用量窗口。primary 是 5 小时窗口、secondary 是 7 天窗口。
type WindowName string

const (
	WindowPrimary   WindowName = "primary"
	WindowSecondary WindowName = "secondary"
)

// Window 是快照里单个用量窗口（迁移 000069 注释约定的 schema，字段名不可改）。
type Window struct {
	// UsedPercent 是窗口已用百分比（0-100，上游口径）。
	UsedPercent float64 `json:"used_percent"`
	// WindowMinutes 是窗口长度（分钟）。
	WindowMinutes int64 `json:"window_minutes,omitempty"`
	// ResetAt 是窗口重置的绝对时刻（unix 秒）；0 表示上游未给出。
	ResetAt int64 `json:"reset_at,omitempty"`
}

// Snapshot 是 subscription_accounts.usage_snapshot 列的持久化形态。任一窗口可缺失（nil）。
type Snapshot struct {
	Primary    *Window   `json:"primary,omitempty"`
	Secondary  *Window   `json:"secondary,omitempty"`
	PlanType   string    `json:"plan_type,omitempty"`
	CapturedAt time.Time `json:"captured_at"`
}

// ParseSnapshot 解析快照列；空值或损坏的 JSON 返回 ok=false。
// 损坏的快照静默忽略：调用方按「无观测」处理，不能因观测数据坏掉阻断选号。
func ParseSnapshot(raw []byte) (Snapshot, bool) {
	if len(raw) == 0 {
		return Snapshot{}, false
	}
	var snapshot Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return Snapshot{}, false
	}
	return snapshot, true
}

// Decision 是一次判定结果。Paused 为 false 时其余字段为零值。
type Decision struct {
	Paused bool
	// Window 是触顶的窗口（primary 优先：它先重置，暂停代价最小）。
	Window WindowName
	// UsedPercent 是触顶窗口的水位。
	UsedPercent float64
	// ResetAtUnix 是应恢复调度的时刻（触顶窗口的重置时刻，unix 秒）。
	ResetAtUnix int64
}

// RemainingMs 是距恢复时刻的毫秒数；未暂停或已到期返回 0。
func (d Decision) RemainingMs(now time.Time) int64 {
	if !d.Paused || d.ResetAtUnix <= 0 {
		return 0
	}
	remaining := time.Unix(d.ResetAtUnix, 0).Sub(now).Milliseconds()
	if remaining <= 0 {
		return 0
	}
	return remaining
}

// Evaluate 按阈值判定快照此刻是否触顶。两个易漏边界（官方核对表）：
//   - 任一窗口可能缺失：缺失视为不限，不得按 0% 或 100% 臆断；
//   - 窗口标称已重置（reset_at <= now）却仍报高水位：观测自相矛盾，不暂停——宁多发一个请求，不锁死账号。
//
// 比较用 >=：阈值 100 意味着只在完全打满时暂停。
func Evaluate(snapshot Snapshot, thresholdPercent int32, now time.Time) Decision {
	if !ValidThreshold(thresholdPercent) {
		thresholdPercent = MaxThresholdPercent
	}
	threshold := float64(thresholdPercent)
	windows := []struct {
		name   WindowName
		window *Window
	}{
		{WindowPrimary, snapshot.Primary},
		{WindowSecondary, snapshot.Secondary},
	}
	nowUnix := now.Unix()
	for _, w := range windows {
		if w.window == nil || w.window.UsedPercent < threshold {
			continue
		}
		if w.window.ResetAt <= nowUnix {
			continue
		}
		return Decision{
			Paused:      true,
			Window:      w.name,
			UsedPercent: w.window.UsedPercent,
			ResetAtUnix: w.window.ResetAt,
		}
	}
	return Decision{}
}
