package adapter

// ProbeInputText 是主动探测发给上游模型的用户输入：最小、协议无关、无边际成本。
// 探测过程要把「请求了什么 → 模型回了什么」完整透出，所以这里定义一次、各协议共用。
const ProbeInputText = "hi"

// ProbeResult 是一次主动探测的内部结果。Facts 只在上游返回可靠 usage 时存在；
// 探测失败仍保留 status，调用方据此写审计和成本风险。
type ProbeResult struct {
	StatusCode int
	Facts      *ResponseFacts
}
