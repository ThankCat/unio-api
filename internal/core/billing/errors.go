package billing

import "errors"

var (
	// ErrInvalidUsage 表示 usage token 数量不满足计费约束。
	ErrInvalidUsage = errors.New("billing: invalid usage")
	// ErrInvalidRate 表示 token 单价快照缺少必需单价或单价无效。
	ErrInvalidRate = errors.New("billing: invalid token rate")
	// ErrInvalidPrice 是旧命名兼容别名，后续新代码优先使用 ErrInvalidRate。
	ErrInvalidPrice = ErrInvalidRate
	// ErrUnsupportedPricingUnit 表示当前 billing service 不支持该计价单位。
	ErrUnsupportedPricingUnit = errors.New("billing: unsupported pricing unit")
	// ErrUnsupportedFormula 表示当前 billing service 不支持该价格计算公式。
	ErrUnsupportedFormula = errors.New("billing: unsupported formula")
	// ErrMissingFxRate 表示售价与成本币种不同但未提供汇率（跨币种比较必须带 fxRate，D5）。
	ErrMissingFxRate = errors.New("billing: sale/cost currency mismatch without fx rate")
)
