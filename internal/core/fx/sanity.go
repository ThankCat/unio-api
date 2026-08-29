package fx

import (
	"fmt"
	"math/big"
)

// 合理性校验（D7）：防错数据优先级高于防宕机——API 宕机只是数据变旧，
// 错数据（0/负数/单位错乱/异常跳变）会静默污染所有毛利计算，必须在入库前拦下。

// quoteBands 是各币种汇率的硬合理区间（1 USD 兑多少目标币）。
// USD/CNY 是有管理的汇率，历史区间远窄于 [5,10]；越界几乎必然是源数据故障。
// 新增币种时在此补区间；未登记的币种跳过区间检查（只做跳变检查）。
var quoteBands = map[string][2]int64{
	"CNY": {5, 10},
}

// maxJumpNum/maxJumpDen = 5%：相邻两次汇率变动超过 5% 拒收。
// USD/CNY 日波动通常 <0.3%，单日 5% 跳变几乎必然是 API 返回垃圾数据而非真实行情。
const (
	maxJumpNum = 5
	maxJumpDen = 100
)

// ValidateRate 校验新汇率的合理性；prev 为 nil 表示首条（跳过跳变检查）。
func ValidateRate(quote string, next *big.Rat, prev *big.Rat) error {
	if next == nil || next.Sign() <= 0 {
		return fmt.Errorf("fx: rate for %s must be positive", quote)
	}
	if band, ok := quoteBands[quote]; ok {
		low := big.NewRat(band[0], 1)
		high := big.NewRat(band[1], 1)
		if next.Cmp(low) < 0 || next.Cmp(high) > 0 {
			return fmt.Errorf("fx: rate %s for %s is outside sane band [%d, %d]", next.FloatString(4), quote, band[0], band[1])
		}
	}
	if prev != nil && prev.Sign() > 0 {
		// |next - prev| / prev > 5% → 拒收。纯有理数比较，无浮点误差。
		diff := new(big.Rat).Sub(next, prev)
		diff.Abs(diff)
		threshold := new(big.Rat).Mul(prev, big.NewRat(maxJumpNum, maxJumpDen))
		if diff.Cmp(threshold) > 0 {
			return fmt.Errorf(
				"fx: rate for %s jumped more than %d%% (prev %s -> next %s)",
				quote, maxJumpNum, prev.FloatString(4), next.FloatString(4),
			)
		}
	}
	return nil
}
