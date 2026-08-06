package adapter

// ProbeResult 是一次主动探测的内部结果。Facts 只在上游返回可靠 usage 时存在；
// 探测失败仍保留 status，调用方据此写审计和成本风险。
type ProbeResult struct {
	StatusCode int
	Facts      *ResponseFacts
}
