package tpmobserver

import (
	"sort"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
)

// scopeWeights 记录一个被跟踪主体（Route 维度按 request、Channel 维度按 attempt）
// 在各分钟上写出的 provisional 观测。可靠 usage 到达后按同一份权重把估算换成实际值，
// 保证「总量准确、分钟分布只依赖真实观察到的 chunk」。
type scopeWeights struct {
	inputRecorded bool
	inputMinute   int64
	inputTokens   int64

	outputByMinute map[int64]int64
	// fallbackMinute 是完全没有输出权重时实际输出的归属分钟（响应完成分钟）。
	fallbackMinute int64
}

func newScopeWeights() *scopeWeights {
	return &scopeWeights{outputByMinute: make(map[int64]int64, 4)}
}

func (w *scopeWeights) addInput(minute, tokens int64) {
	// 输入只记一次：fallback 时 Route 不重复记，同一 attempt 也不会重复写入。
	if w.inputRecorded {
		return
	}
	w.inputRecorded = true
	w.inputMinute = minute
	w.inputTokens = tokens
}

func (w *scopeWeights) addOutput(minute, tokens int64) {
	if tokens <= 0 {
		return
	}
	w.outputByMinute[minute] += tokens
	w.fallbackMinute = minute
}

// noteCompletion 记下响应完成分钟，供「有可靠输出但一个输出 chunk 都没观察到」时兜底。
func (w *scopeWeights) noteCompletion(minute int64) {
	if len(w.outputByMinute) == 0 {
		w.fallbackMinute = minute
	}
}

// corrections 把可靠 usage 换算成各分钟的有符号增量（§8.4）。
//
// 输入差额落在输入原始分钟；实际输出按各分钟已观察到的 provisional 权重等比分配，
// 最后一个分钟承担整数除法余数，保证分钟合计严格等于实际输出。
// 早于 oldestAllowedMinute 的分钟直接丢弃并计入 expired——超出回溯窗口的修正不值得再写，
// 而且目标桶大概率已经过期，重建它只会让 Admin 读到一个只含负增量的假分钟。
func (w *scopeWeights) corrections(
	kind breakerstore.TPMObservationKind,
	id int64,
	actualInput int64,
	actualOutput int64,
	oldestAllowedMinute int64,
) (entries []breakerstore.TPMObservationEntry, expired int64) {
	byMinute := make(map[int64]breakerstore.TPMObservationDelta, len(w.outputByMinute)+1)

	if w.inputRecorded {
		delta := byMinute[w.inputMinute]
		delta.InputTokens += actualInput - w.inputTokens
		delta.ProvisionalTokens -= w.inputTokens
		byMinute[w.inputMinute] = delta
	}

	minutes := make([]int64, 0, len(w.outputByMinute))
	total := int64(0)
	for minute, tokens := range w.outputByMinute {
		minutes = append(minutes, minute)
		total += tokens
	}
	sort.Slice(minutes, func(i, j int) bool { return minutes[i] < minutes[j] })

	switch {
	case total > 0:
		remaining := actualOutput
		for index, minute := range minutes {
			provisional := w.outputByMinute[minute]
			allocated := remaining
			if index < len(minutes)-1 {
				allocated = actualOutput * provisional / total
				remaining -= allocated
			}
			delta := byMinute[minute]
			delta.OutputTokens += allocated - provisional
			delta.ProvisionalTokens -= provisional
			byMinute[minute] = delta
		}
	case actualOutput > 0 && w.fallbackMinute > 0:
		// 上游 usage 含无法从流里观察到的输出（例如隐藏 reasoning）时，全部归入响应完成分钟。
		delta := byMinute[w.fallbackMinute]
		delta.OutputTokens += actualOutput
		byMinute[w.fallbackMinute] = delta
	}

	ordered := make([]int64, 0, len(byMinute))
	for minute := range byMinute {
		ordered = append(ordered, minute)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })

	entries = make([]breakerstore.TPMObservationEntry, 0, len(ordered))
	for _, minute := range ordered {
		if minute < oldestAllowedMinute {
			expired++
			continue
		}
		delta := byMinute[minute]
		if delta.Empty() {
			continue
		}
		entries = append(entries, breakerstore.TPMObservationEntry{
			Scope: breakerstore.TPMObservationScope{Kind: kind, ID: id, Minute: minute},
			Delta: delta,
		})
	}
	return entries, expired
}
