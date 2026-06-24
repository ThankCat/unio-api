// Package opsutil 收拢各 admin 运维聚合 service（渠道/服务商/模型/线路/客户）共用的
// pgtype 取值转换、健康分桶、成功率与错误包装小工具，避免每个包重复实现。
package opsutil

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/platform/failure"
)

// 健康分桶阈值（按区间内成功率），与概览/渠道一致。
const (
	HealthyThreshold  = 0.95
	DegradedThreshold = 0.80
)

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

// Int8Narg 把 nil 转成 SQL NULL（不过滤）。
func Int8Narg(p *int64) pgtype.Int8 {
	if p == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *p, Valid: true}
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
func HealthBucket(succeeded, total int64) string {
	if total == 0 {
		return "no_data"
	}
	rate := float64(succeeded) / float64(total)
	switch {
	case rate >= HealthyThreshold:
		return "healthy"
	case rate >= DegradedThreshold:
		return "degraded"
	default:
		return "unhealthy"
	}
}

// StoreFailed 包装存储错误为 admin_store_failed。
func StoreFailed(cause error, message string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, cause, failure.WithMessage(message))
}

// InvalidArgument 构造 admin_invalid_argument。
func InvalidArgument(field, message string) error {
	return failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage(message), failure.WithField("field", field))
}
