package ledger

import (
	"math/big"

	"github.com/jackc/pgx/v5/pgtype"
)

// sameNumeric 比较两个 pgtype.Numeric 是否表示同一个金额值。
func sameNumeric(left pgtype.Numeric, right pgtype.Numeric) bool {
	leftRat, leftOK := numericRat(left)
	rightRat, rightOK := numericRat(right)
	if !leftOK || !rightOK {
		return leftOK == rightOK
	}

	return leftRat.Cmp(rightRat) == 0
}

// numericRat 将 pgtype.Numeric 转成有理数，用于金额等值比较。
func numericRat(value pgtype.Numeric) (*big.Rat, bool) {
	if !value.Valid || value.NaN || value.InfinityModifier != pgtype.Finite || value.Int == nil {
		return nil, false
	}

	rat := new(big.Rat).SetInt(new(big.Int).Set(value.Int))
	if value.Exp > 0 {
		rat.Mul(rat, new(big.Rat).SetInt(pow10(value.Exp)))
	}
	if value.Exp < 0 {
		rat.Quo(rat, new(big.Rat).SetInt(pow10(-value.Exp)))
	}

	return rat, true
}

// pow10 返回 10 的 exp 次方。
func pow10(exp int32) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exp)), nil)
}

// isPositiveNumeric 判断是否是正数
func isPositiveNumeric(value pgtype.Numeric) bool {
	rat, ok := numericRat(value)
	return ok && rat.Sign() > 0
}

// numericLessOrEqual 判断 left 是否小于等于 right
func numericLessOrEqual(left pgtype.Numeric, right pgtype.Numeric) bool {
	leftRat, leftOK := numericRat(left)
	rightRat, rightOK := numericRat(right)
	if !leftOK || !rightOK {
		return false
	}

	return leftRat.Cmp(rightRat) <= 0
}

// numericGreaterThan 判断 left 是否大于 right
func numericGreaterThan(left pgtype.Numeric, right pgtype.Numeric) bool {
	leftRat, leftOK := numericRat(left)
	rightRat, rightOK := numericRat(right)
	if !leftOK || !rightOK {
		return false
	}

	return leftRat.Cmp(rightRat) > 0
}

// numericMin 获取最小值
func numericMin(left pgtype.Numeric, right pgtype.Numeric) pgtype.Numeric {
	if numericLessOrEqual(left, right) {
		return left
	}

	return right
}

// numericZero 返回表示 0 的 NUMERIC。
func numericZero() pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(0), Exp: 0, Valid: true}
}

// numericFinite 判断 NUMERIC 是否为有限可计算金额。
func numericFinite(value pgtype.Numeric) bool {
	return value.Valid && !value.NaN && value.InfinityModifier == pgtype.Finite && value.Int != nil
}

// numericAdd 精确相加两个有限 NUMERIC（按更小指数对齐，不引入浮点误差）。
// 任一操作数非有限时返回无效 NUMERIC。
func numericAdd(left pgtype.Numeric, right pgtype.Numeric) pgtype.Numeric {
	if !numericFinite(left) || !numericFinite(right) {
		return pgtype.Numeric{}
	}

	exp := left.Exp
	if right.Exp < exp {
		exp = right.Exp
	}

	sum := new(big.Int).Add(scaledNumericInt(left, exp), scaledNumericInt(right, exp))
	return pgtype.Numeric{Int: sum, Exp: exp, Valid: true}
}

// scaledNumericInt 返回 value.Int 放大到 targetExp 指数下的整数表示，要求 targetExp <= value.Exp。
func scaledNumericInt(value pgtype.Numeric, targetExp int32) *big.Int {
	diff := value.Exp - targetExp
	if diff <= 0 {
		return new(big.Int).Set(value.Int)
	}

	return new(big.Int).Mul(new(big.Int).Set(value.Int), pow10(diff))
}
