package dashboard

import (
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// tsNarg 把可选时间过滤值转成 pgtype.Timestamptz：零值表示不过滤（SQL NULL）。
func tsNarg(t time.Time) pgtype.Timestamptz {
	if t.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// numericString 把 NUMERIC 精确格式化为十进制字符串（不用 float）；NULL/NaN/Inf → "0"。
func numericString(n pgtype.Numeric) string {
	if !n.Valid || n.NaN || n.InfinityModifier != pgtype.Finite {
		return "0"
	}
	if n.Int == nil {
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

// subtractDecimal 用 big.Rat 精确相减两个十进制字符串（毛利 = 收入 − 成本），避免 float 误差。
// 非法输入按 0 处理；结果保留至 10 位小数（与 NUMERIC(20,10) 一致）后去尾零。
func subtractDecimal(a, b string) string {
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

// addDecimal 用 big.Rat 精确相加两个十进制字符串，避免 float 误差。
func addDecimal(a, b string) string {
	ra, ok := new(big.Rat).SetString(a)
	if !ok {
		ra = new(big.Rat)
	}
	rb, ok := new(big.Rat).SetString(b)
	if !ok {
		rb = new(big.Rat)
	}
	return trimDecimalString(new(big.Rat).Add(ra, rb).FloatString(10))
}

// trimDecimalString 去掉十进制字符串多余的尾零与小数点："0.5300000000" → "0.53"，"-0" → "0"。
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

func invalidArgument(field, message string) error {
	return failure.New(
		failure.CodeAdminInvalidArgument,
		failure.WithMessage(message),
		failure.WithField("field", field),
	)
}

func storeFailed(cause error, message string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, cause, failure.WithMessage(message))
}
