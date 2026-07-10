// Package opsutil 收拢各 admin 运维聚合 service（渠道/服务商/模型/线路/客户）共用的
// pgtype 取值转换、健康分桶、成功率与错误包装小工具，避免每个包重复实现。
package opsutil

import (
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/platform/failure"
)

// 健康分桶阈值已迁移为运行时配置 admin_backend.channel_health_thresholds
// (appsettings.AdminBackendChannelHealthThresholds),调用方读取后显式传入 HealthBucket——
// opsutil 保持纯函数、不依赖 appsettings。

// TsNarg 把可选时间过滤值转成 pgtype.Timestamptz：零值表示不过滤（SQL NULL）。
func TsNarg(t time.Time) pgtype.Timestamptz {
	if t.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// TextNarg 把空串转成 SQL NULL（不过滤）。
func TextNarg(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// BoolNarg 把 bool 转成 pgtype.Bool（用于 sort_desc 等必填布尔参数）。
func BoolNarg(v bool) pgtype.Bool {
	return pgtype.Bool{Bool: v, Valid: true}
}

// Int8Arg 构造非空 pgtype.Int8（用于必填 bigint 参数被 sqlc 推断为 nullable 的场景）。
func Int8Arg(v int64) pgtype.Int8 {
	return pgtype.Int8{Int64: v, Valid: true}
}

// TextValue 取 pgtype.Text 值，NULL → ""。
func TextValue(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}

// TimeValue 取 pgtype.Timestamptz 值，NULL → nil。
func TimeValue(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	tt := t.Time
	return &tt
}

// Int4Value 取 pgtype.Int4 值，NULL → nil。
func Int4Value(v pgtype.Int4) *int32 {
	if !v.Valid {
		return nil
	}
	n := v.Int32
	return &n
}

// Int8Value 取 pgtype.Int8 值，NULL → nil。
func Int8Value(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

// SuccessRate 计算成功率，分母为 0 → 0。
func SuccessRate(succeeded, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(succeeded) / float64(total)
}

// HealthBucket 按成功率与样本量分桶：healthy / degraded / unhealthy / no_data。
// 阈值(healthyRate/degradedRate)来自运行时配置,由调用方传入(须满足 0 < degraded < healthy <= 1,
// 写入口已校验,此处不重复防御)。
func HealthBucket(succeeded, total int64, healthyRate, degradedRate float64) string {
	if total == 0 {
		return "no_data"
	}
	rate := float64(succeeded) / float64(total)
	switch {
	case rate >= healthyRate:
		return "healthy"
	case rate >= degradedRate:
		return "degraded"
	default:
		return "unhealthy"
	}
}

// NumericString 把 NUMERIC 精确格式化为十进制字符串（不经 float）；NULL/NaN/Inf → "0"。
func NumericString(n pgtype.Numeric) string {
	if !n.Valid || n.NaN || n.InfinityModifier != pgtype.Finite || n.Int == nil {
		return "0"
	}
	negative := n.Int.Sign() < 0
	digits := new(big.Int).Abs(n.Int).String()
	exp := int(n.Exp)
	var formatted string
	switch {
	case exp == 0:
		formatted = digits
	case exp > 0:
		formatted = digits + strings.Repeat("0", exp)
	default:
		scale := -exp
		if len(digits) <= scale {
			digits = strings.Repeat("0", scale-len(digits)+1) + digits
		}
		point := len(digits) - scale
		formatted = digits[:point] + "." + digits[point:]
	}
	if negative {
		formatted = "-" + formatted
	}
	return trimDecimalString(formatted)
}

// NumericStringPtr 把可空 NUMERIC 精确格式化为十进制字符串指针；NULL/NaN/Inf → nil（区别于 NumericString 的 "0"）。
func NumericStringPtr(n pgtype.Numeric) *string {
	if !n.Valid || n.NaN || n.InfinityModifier != pgtype.Finite || n.Int == nil {
		return nil
	}
	s := NumericString(n)
	return &s
}

// SubtractDecimal 用 big.Rat 精确相减两个十进制字符串，保留 10 位小数后去尾零。
func SubtractDecimal(a, b string) string {
	ra, ok := new(big.Rat).SetString(a)
	if !ok {
		ra = new(big.Rat)
	}
	rb, ok := new(big.Rat).SetString(b)
	if !ok {
		rb = new(big.Rat)
	}
	return trimDecimalString(new(big.Rat).Sub(ra, rb).FloatString(10))
}

// Ratio 计算 numerator/denominator 的 float64 比例；分母 ≤ 0 → 0。
func Ratio(numerator, denominator string) float64 {
	num, ok := new(big.Rat).SetString(numerator)
	if !ok {
		return 0
	}
	den, ok := new(big.Rat).SetString(denominator)
	if !ok || den.Sign() <= 0 {
		return 0
	}
	f, _ := new(big.Rat).Quo(num, den).Float64()
	return f
}

func trimDecimalString(s string) string {
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	if s == "" || s == "-" || s == "-0" {
		return "0"
	}
	return s
}

// StoreFailed 包装存储错误为 admin_store_failed。
func StoreFailed(cause error, message string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, cause, failure.WithMessage(message))
}

// InvalidArgument 构造 admin_invalid_argument。
func InvalidArgument(field, message string) error {
	return failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage(message), failure.WithField("field", field))
}

// LatencyStats 是 attempt 粒度延迟分位画像（毫秒）。
// Sample = 区间内测到延迟（成功且 completed_at 非空）的 attempt 数；
// Coverage = Sample / 成功 attempt，反映平均/分位的代表性。
type LatencyStats struct {
	Avg      float64
	P50      float64
	P90      float64
	P95      float64
	P99      float64
	Sample   int64
	Coverage float64
}

// AttemptLatency 从 SQL 聚合字段组装延迟画像。
func AttemptLatency(avg, p50, p90, p95, p99 float64, sample, succeeded int64) LatencyStats {
	s := LatencyStats{Avg: avg, P50: p50, P90: p90, P95: p95, P99: p99, Sample: sample}
	if succeeded > 0 {
		s.Coverage = float64(sample) / float64(succeeded)
	}
	return s
}
