package billing

import (
	"math/big"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/jackc/pgx/v5/pgtype"
)

// requiredNonNegativeNumeric 将必填 NUMERIC 单价转换成非负有理数。
func requiredNonNegativeNumeric(value pgtype.Numeric) (*big.Rat, error) {
	rat, err := numericToRat(value)
	if err != nil {
		return nil, err
	}

	if rat.Sign() < 0 {
		return nil, failure.Wrap(
			failure.CodeBillingInvalidPrice,
			ErrInvalidRate,
			failure.WithMessage(ErrInvalidRate.Error()),
		)
	}

	return rat, nil
}

// requiredPositiveNumeric 将必填 NUMERIC 转换成正有理数（长上下文倍率等，须 > 0）。
func requiredPositiveNumeric(value pgtype.Numeric) (*big.Rat, error) {
	rat, err := requiredNonNegativeNumeric(value)
	if err != nil {
		return nil, err
	}
	if rat.Sign() <= 0 {
		return nil, failure.Wrap(
			failure.CodeBillingInvalidPrice,
			ErrInvalidRate,
			failure.WithMessage(ErrInvalidRate.Error()),
		)
	}
	return rat, nil
}

// numericToRat 将 pgtype.Numeric 转成 big.Rat，避免 float64 精度损失。
func numericToRat(value pgtype.Numeric) (*big.Rat, error) {
	if !value.Valid || value.NaN || value.InfinityModifier != pgtype.Finite {
		return nil, failure.Wrap(
			failure.CodeBillingInvalidPrice,
			ErrInvalidRate,
			failure.WithMessage(ErrInvalidRate.Error()),
		)
	}
	if value.Int == nil {
		return new(big.Rat), nil
	}

	rat := new(big.Rat).SetInt(new(big.Int).Set(value.Int))
	if value.Exp > 0 {
		rat.Mul(rat, new(big.Rat).SetInt(pow10(value.Exp)))
	}
	if value.Exp < 0 {
		rat.Quo(rat, new(big.Rat).SetInt(pow10(-value.Exp)))
	}

	return rat, nil
}

// tokenCost 计算某类 token 在除以 100 万之前的原始金额。
func tokenCost(unitPrice *big.Rat, tokens int64) *big.Rat {
	return new(big.Rat).Mul(unitPrice, big.NewRat(tokens, 1))
}

// ratToNumeric 将金额四舍五入到固定小数位，匹配 NUMERIC(20,10)。
func ratToNumeric(value *big.Rat, scale int32) pgtype.Numeric {
	multiplier := pow10(scale)
	scaled := new(big.Rat).Mul(value, new(big.Rat).SetInt(multiplier))

	return pgtype.Numeric{
		Int:   roundHalfUp(scaled),
		Exp:   -scale,
		Valid: true,
	}
}

// roundHalfUp 对非负有理数执行四舍五入。
func roundHalfUp(value *big.Rat) *big.Int {
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(value.Num(), value.Denom(), remainder)

	if new(big.Int).Mul(remainder, big.NewInt(2)).Cmp(value.Denom()) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}

	return quotient
}

// pow10 返回 10 的 exp 次方。
func pow10(exp int32) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exp)), nil)
}

// maxRat 返回两个非 nil 有理数中的较大值。
func maxRat(left *big.Rat, right *big.Rat) *big.Rat {
	if left.Cmp(right) >= 0 {
		return new(big.Rat).Set(left)
	}

	return new(big.Rat).Set(right)
}
