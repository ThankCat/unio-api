// Package query 编排 admin 管理端（M6 只读查询台）的请求 / 用量 / 账本只读读取。
//
// 全部只读，不改任何状态。安全红线（见 ADMIN_MODULES_DRAFT M6）：
//   - 列表绝不返回 internal_error_detail（list SQL 不 SELECT 该列，从存储层就脱敏）。
//   - 详情默认也脱敏 internal_error_detail；仅当调用方显式 includeInternal=true 才回显，
//     用于平台管理员排查（handler 由 ?include_internal=true 控制）。
//   - 金额一律用十进制字符串承载，绝不经过 float（与 price/costprice 一致）。
package query

import (
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// int8Narg 把可选 int64 过滤值转成 pgtype.Int8：nil 表示不过滤（SQL NULL）。
func int8Narg(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}

// textNarg 把可选字符串过滤值转成 pgtype.Text：空串表示不过滤（SQL NULL）。
func textNarg(s string) pgtype.Text {
	s = strings.TrimSpace(s)
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// tsNarg 把可选时间过滤值转成 pgtype.Timestamptz：nil 表示不过滤（SQL NULL）。
func tsNarg(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

// boolNarg 把 bool 转成 pgtype.Bool（用于 sort_desc 等必填布尔参数）。
func boolNarg(v bool) pgtype.Bool {
	return pgtype.Bool{Bool: v, Valid: true}
}

// int8Ptr 把可空 pgtype.Int8 转成 *int64：NULL → nil。
func int8Ptr(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	out := v.Int64
	return &out
}

// int4Ptr 把可空 pgtype.Int4 转成 *int32：NULL → nil。
func int4Ptr(v pgtype.Int4) *int32 {
	if !v.Valid {
		return nil
	}
	out := v.Int32
	return &out
}

// textPtr 把可空 pgtype.Text 转成 *string：NULL → nil。
func textPtr(v pgtype.Text) *string {
	if !v.Valid {
		return nil
	}
	out := v.String
	return &out
}

// timePtr 把可空 pgtype.Timestamptz 转成 *time.Time：NULL → nil。
func timePtr(v pgtype.Timestamptz) *time.Time {
	if !v.Valid {
		return nil
	}
	out := v.Time
	return &out
}

// numericString 把 NUMERIC 精确格式化为十进制字符串（不用 float）；NULL/NaN/Inf → "0"。
func numericString(n pgtype.Numeric) string {
	if s := numericPtr(n); s != nil {
		return *s
	}
	return "0"
}

// numericPtr 把 NUMERIC 精确格式化为十进制字符串（不用 float）；NULL/NaN/Inf 返回 nil。
func numericPtr(n pgtype.Numeric) *string {
	if !n.Valid || n.NaN || n.InfinityModifier != pgtype.Finite {
		return nil
	}
	if n.Int == nil {
		zero := "0"
		return &zero
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
	return &formatted
}

func invalidArgument(field, message string) error {
	return failure.New(
		failure.CodeAdminInvalidArgument,
		failure.WithMessage(message),
		failure.WithField("field", field),
	)
}

func notFound(message string) error {
	return failure.New(failure.CodeAdminNotFound, failure.WithMessage(message))
}

func storeFailed(cause error, message string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, cause, failure.WithMessage(message))
}
