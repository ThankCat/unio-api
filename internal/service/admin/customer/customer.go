// Package customer 编排 admin 管理端（M7）的用户 / 项目 / API Key / 手工调额。
//
// 设计要点（见 ADMIN_MODULES_DRAFT M7 + DEC-017）：
//   - 项目仅作工作空间，不承载启停/预算/策略；费用上限挂在 API Key（生命周期累计封顶）。
//   - 余额变动一律走 core/ledger（写 adjustment_* 流水 + 幂等），禁止直接改 user_balances。
//   - API Key 明文只在创建时返回一次；列表/详情绝不回 key_hash。
//   - 金额一律用十进制字符串承载，绝不经过 float。
package customer

import (
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/platform/failure"
)

// moneyPattern 限定非负十进制金额字符串。
var moneyPattern = regexp.MustCompile(`^\d+(\.\d+)?$`)

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

// parseMoney 解析必填正向金额：非负十进制字符串 → pgtype.Numeric。
func parseMoney(field, raw string) (pgtype.Numeric, error) {
	s := strings.TrimSpace(raw)
	if !moneyPattern.MatchString(s) {
		return pgtype.Numeric{}, invalidArgument(field, "must be a non-negative decimal amount")
	}
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		return pgtype.Numeric{}, invalidArgument(field, "invalid decimal amount")
	}
	return n, nil
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
