// Package appsettings 是「后台可编辑、服务免重启生效、可观测」的运行时配置系统。
//
// 三层:
//   - 存储:PostgreSQL app_settings(key→JSONB + description),权威源。
//   - 分发/缓存:Redis 实时缓存(跨进程秒级生效、值可直接在 Redis 观测)+ 各进程本地短缓存(热路径去抖)。
//   - 注册表:每个配置项在 registry 声明 key/分类/说明/默认/校验/是否热改;admin 面板与后端读取都据此驱动。
//
// 首个接入项是 Anthropic beta 转发策略(见 beta_policy.go)。新增配置 = 在 registry 注册一条 + 读取访问器。
package appsettings

import (
	"encoding/json"
	"fmt"
)

// Definition 描述一个可运行时配置的设置项(注册表条目)。
type Definition struct {
	// Key 是 app_settings 主键,建议 "<provider|domain>.<name>",如 "anthropic.beta_policy"。
	Key string
	// Category 是分组标识(admin 面板分组),如 "anthropic"。
	Category string
	// Label 是人类可读标签。
	Label string
	// Description 是配置含义说明;写库时落到 app_settings.description,admin 面板展示。
	Description string
	// HotReload 表示是否免重启生效(true=消费进程读时现取;false=仅展示,改后需重启)。
	HotReload bool
	// Default 是 DB 无记录时的默认值(规范 JSON)。
	Default json.RawMessage
	// Validate 在写入前校验值合法性;nil 表示不校验。
	Validate func(json.RawMessage) error
}

// Registry 是所有可配置设置项的中央登记表。
type Registry struct {
	defs  map[string]Definition
	order []string
}

// NewRegistry 用给定定义构建注册表(key 重复会 panic,属开发期错误)。
func NewRegistry(defs ...Definition) *Registry {
	r := &Registry{defs: make(map[string]Definition, len(defs))}
	for _, d := range defs {
		if _, dup := r.defs[d.Key]; dup {
			panic(fmt.Sprintf("appsettings: duplicate setting key %q", d.Key))
		}
		r.defs[d.Key] = d
		r.order = append(r.order, d.Key)
	}
	return r
}

// Get 返回指定 key 的定义;ok=false 表示未注册。
func (r *Registry) Get(key string) (Definition, bool) {
	d, ok := r.defs[key]
	return d, ok
}

// List 按注册顺序返回全部定义。
func (r *Registry) List() []Definition {
	out := make([]Definition, 0, len(r.order))
	for _, k := range r.order {
		out = append(out, r.defs[k])
	}
	return out
}

// DefaultRegistry 构建当前进程支持的配置注册表。
//
// 新配置在此追加 Definition 即可(启动 seed 会把缺行的默认值写入 DB,admin 面板自动出现)。
// 域约定:Category 必须与 key 前缀(首个 "." 之前)一致——gateway / anthropic /
// admin_backend / admin_frontend 四域,消费方互不加载对方的域(单测断言此约定)。
func DefaultRegistry() *Registry {
	return NewRegistry(
		betaPolicyDefinition(),
		circuitBreakerDefinition(),
		rateLimitDefaultsDefinition(),
		streamIdleTimeoutDefinition(),
		channelCooldownDefinition(),
		credential401ThresholdDefinition(),
		defaultChannelTimeoutDefinition(),
		failureCooldownDefinition(),
		concurrencyDefaultsDefinition(),
		channelHealthThresholdsDefinition(),
		dashboardThresholdsDefinition(),
	)
}
