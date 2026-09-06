// Package proxyclient 按代理 URL 缓存 *http.Client（边界 29：按账号代理）。
//
// 订阅账号可各自绑定出口代理；transport 按代理 URL 为键缓存复用，避免每请求新建连接池。
// adapter 不自管 client 池——bootstrap 用本包构造解析器注入，导入、令牌刷新、正式请求
// 三条路径共用同一份缓存，保证同一个号始终从同一出口访问上游（风控一致性）。
// 「账号代理 → 渠道代理 → 直连」的回退决策不在本包：由 channel.Runtime.OutboundProxyURL 统一给出。
package proxyclient

import (
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// ErrInvalidProxyURL 表示运行期拿到的代理 URL 无法解析为「scheme://host」形态。
// 错误文本固定不回显 URL 本身（代理 URL 可能携带 userinfo 凭据）。
var ErrInvalidProxyURL = errors.New("proxyclient: invalid proxy url")

// Resolver 按代理 URL 解析 HTTP client。空串返回直连缺省 client。
//
// 缓存键空间 = 曾经配置过的代理 URL 集合，随代理实体数有界增长；条目不主动淘汰
// （代理删除/禁用后该 URL 不再被任何账号或渠道引用，条目仅占用少量空闲连接池）。
type Resolver struct {
	mu       sync.RWMutex
	direct   *http.Client
	byProxy  map[string]*http.Client
	template func() *http.Transport
}

// NewResolver 创建解析器。direct 是无代理时使用的缺省 client（通常为 bootstrap 共享 client），
// 不可为 nil。代理 client 的 transport 以合理的连接池默认值独立创建。
func NewResolver(direct *http.Client) *Resolver {
	if direct == nil {
		direct = http.DefaultClient
	}
	return &Resolver{
		direct:  direct,
		byProxy: make(map[string]*http.Client),
		template: func() *http.Transport {
			return &http.Transport{
				MaxIdleConns:          32,
				MaxIdleConnsPerHost:   8,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: time.Second,
				ForceAttemptHTTP2:     true,
			}
		},
	}
}

// ClientFor 返回该代理 URL 的 client。
//
// 解析失败 fail-closed：返回一个所有请求都以 ErrInvalidProxyURL 失败的 client，而不是回退直连。
// 号池场景「同号同出口」是风控约束，静默直连恰恰破坏它；非法代理 URL 会在导入/编辑入口被校验拒绝，
// 运行期出现即为数据异常，让请求明确失败比换出口访问上游更安全。
func (r *Resolver) ClientFor(proxyURL string) *http.Client {
	if proxyURL == "" {
		return r.direct
	}
	r.mu.RLock()
	client, ok := r.byProxy[proxyURL]
	r.mu.RUnlock()
	if ok {
		return client
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if cached, exists := r.byProxy[proxyURL]; exists {
		return cached
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		failing := &http.Client{Transport: failingTransport{err: ErrInvalidProxyURL}}
		r.byProxy[proxyURL] = failing
		return failing
	}
	transport := r.template()
	transport.Proxy = http.ProxyURL(parsed)
	// 与直连 client 一致：整体超时由调用方 context 控制，这里只继承 Timeout（通常为 0），
	// 避免与流式长连接的 idle 看门狗冲突。
	created := &http.Client{Transport: transport, Timeout: r.direct.Timeout}
	r.byProxy[proxyURL] = created
	return created
}

// failingTransport 让非法代理 URL 的每次出站都在请求路径上以固定错误失败。
type failingTransport struct{ err error }

func (t failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, t.err
}
