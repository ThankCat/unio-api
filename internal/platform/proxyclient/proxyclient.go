// Package proxyclient 按代理 URL 缓存 *http.Client（边界 29：按账号代理）。
//
// 订阅账号可各自绑定出口代理；transport 按代理 URL 为键缓存复用，避免每请求新建连接池。
// adapter 不自管 client 池——bootstrap 用本包构造解析器注入，导入、令牌刷新、正式请求
// 三条路径共用同一份缓存，保证同一个号始终从同一出口访问上游（风控一致性）。
package proxyclient

import (
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Resolver 按代理 URL 解析 HTTP client。空串返回直连缺省 client。
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

// ClientFor 返回该代理 URL 的 client；解析失败回退直连（错误在调用方的请求路径上自然暴露，
// 这里不吞：非法代理 URL 会在导入/编辑入口被校验拒绝，运行期出现即为数据异常，直连是最保守降级）。
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

	parsed, err := url.Parse(proxyURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return r.direct
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if cached, exists := r.byProxy[proxyURL]; exists {
		return cached
	}
	transport := r.template()
	transport.Proxy = http.ProxyURL(parsed)
	// 与直连 client 一致：整体超时由调用方 context 控制，这里只继承 Timeout（通常为 0），
	// 避免与流式长连接的 idle 看门狗冲突。
	created := &http.Client{Transport: transport, Timeout: r.direct.Timeout}
	r.byProxy[proxyURL] = created
	return created
}

// RuntimeProxyURL 执行出站代理回退链：账号代理 → 渠道代理 → 空串（直连）。
// 三条账号路径与渠道出站（正式请求/检测/发现/验证）统一走这一条决策。
func RuntimeProxyURL(accountProxy, channelProxy string) string {
	if accountProxy != "" {
		return accountProxy
	}
	return channelProxy
}
