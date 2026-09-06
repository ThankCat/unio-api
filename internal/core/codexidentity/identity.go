// Package codexidentity 定义 Unio 对 Codex 订阅后端声明的客户端身份。
//
// 身份是 originator / User-Agent / version 三个头组成的一组事实，上游会校验 originator 与 UA 首段配套，
// 并在容量紧张时按客户端身份分优先级降载（陈旧版本先被丢弃）。三者必须同源派生：推理面
// （/backend-api/codex/responses）、模型发现（/backend-api/codex/models）与凭据面（auth.openai.com
// 换码/刷新）共用本包，任何一处不得再各自拼字符串。
//
// UA 形态照 0.152.1 真机抓包（sandbox/codex/wire/samples/ingress-request.json）：
//
//	codex-tui/<ver> (Mac OS 15.2.0; arm64) tmux/3.7c (codex-tui; <ver>)
//
// 客户端自报的 UA 只进本地日志，永不出站：号池多客户共号，透传会让一个账号在上游呈现多台设备。
package codexidentity

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

const (
	// Originator 是 Codex TUI 的 originator；0.152.1 抓包与官方 codex-rs 当前默认一致。
	// 旧名 codex_cli_rs 已被官方替换，Sub2API 也只保留其兼容识别。
	Originator = "codex-tui"

	// BaselineVersion 是 wire 契约基线版本（sandbox/codex/wire/0.152.1.json 据此抓取），
	// 既是编译期兜底，也是生效版本的下限：声明比基线更旧的版本没有任何收益。
	BaselineVersion = "0.152.1"

	osSegment       = "Mac OS 15.2.0"
	archSegment     = "arm64"
	terminalSegment = "tmux/3.7c"
)

// versionPattern 只接受官方版本形态：0.152.1、0.153.0-alpha.5。
// 版本号会被拼进出站头，拒绝任意字节，避免自动同步或后台误填把不可控内容送到上游。
var versionPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,3}(-[0-9A-Za-z.]+)?$`)

// Identity 是一组自洽的出站身份事实。
type Identity struct {
	Version string
}

// Default 返回基线身份。
func Default() Identity {
	return Identity{Version: BaselineVersion}
}

// WithVersion 按给定版本构造身份；非法或低于基线的版本回落基线，保证出站永不声明陈旧身份。
func WithVersion(version string) Identity {
	return Identity{Version: FloorVersion(version)}
}

// VersionSource 提供当前生效的客户端版本（Admin 覆写 → 自动同步 → 基线）；nil 等价于基线。
type VersionSource func() string

// Resolve 用版本来源构造当前身份。
func Resolve(source VersionSource) Identity {
	if source == nil {
		return Default()
	}
	return WithVersion(source())
}

// UserAgent 渲染 User-Agent；尾部括号组 (originator; version) 与首段是同一个版本声明的两个出口。
func (id Identity) UserAgent() string {
	return Originator + "/" + id.Version + " (" + osSegment + "; " + archSegment + ") " +
		terminalSegment + " (" + Originator + "; " + id.Version + ")"
}

// ApplyInferenceHeaders 写推理面三个头（/backend-api/codex/*）。
func (id Identity) ApplyInferenceHeaders(h http.Header) {
	h.Set("originator", Originator)
	h.Set("User-Agent", id.UserAgent())
	h.Set("version", id.Version)
}

// ApplyUsageHeaders 写用量面身份头（/backend-api/wham/*：主动查用量、重置卡明细与消费）。
// 与推理面同一组三头：2026-09-06 以 codex-tui 身份实测 200（sandbox/codex/wire/samples/upstream-wham-*.json），
// 不需要 Codex Desktop 那套 openai-beta 头。单独命名是为了让第四个出站面也从本包取身份，而不是各自拼。
func (id Identity) ApplyUsageHeaders(h http.Header) {
	id.ApplyInferenceHeaders(h)
}

// ApplyCredentialHeaders 写凭据面身份对（auth.openai.com 换码/刷新）。
//
// 真实客户端在该面只带 originator 与 User-Agent（codex-rs login/default_client.rs），
// version 门槛只存在于推理面；只发 UA 不发 originator 是没有任何真实客户端会发的半身份。
func (id Identity) ApplyCredentialHeaders(h http.Header) {
	h.Set("originator", Originator)
	h.Set("User-Agent", id.UserAgent())
}

// NormalizeVersion 校验并修剪版本号，非法值返回空串。
func NormalizeVersion(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 64 || !versionPattern.MatchString(raw) {
		return ""
	}
	return raw
}

// FloorVersion 把非法或低于基线的版本收敛到基线。
func FloorVersion(raw string) string {
	version := NormalizeVersion(raw)
	if version == "" || CompareVersions(version, BaselineVersion) < 0 {
		return BaselineVersion
	}
	return version
}

// EffectiveVersion 按「Admin 覆写 → 自动同步值 → 基线」解析生效版本，并施加下限。
// autoSync 为 false 时忽略同步值。
func EffectiveVersion(override string, autoSync bool, synced string) string {
	if v := NormalizeVersion(override); v != "" {
		return FloorVersion(v)
	}
	if autoSync {
		if v := NormalizeVersion(synced); v != "" {
			return FloorVersion(v)
		}
	}
	return BaselineVersion
}

// CompareVersions 比较两个官方形态版本号：数字段逐段比较，正式版高于同号预发布，
// 预发布后缀按字典序。非法输入按 0 处理（调用方应先 NormalizeVersion）。
func CompareVersions(a, b string) int {
	aCore, aPre := splitVersion(a)
	bCore, bPre := splitVersion(b)
	for i := 0; i < len(aCore) || i < len(bCore); i++ {
		var x, y int
		if i < len(aCore) {
			x = aCore[i]
		}
		if i < len(bCore) {
			y = bCore[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case aPre == bPre:
		return 0
	case aPre == "":
		return 1
	case bPre == "":
		return -1
	default:
		return strings.Compare(aPre, bPre)
	}
}

func splitVersion(v string) ([]int, string) {
	v = strings.TrimSpace(v)
	pre := ""
	if idx := strings.IndexByte(v, '-'); idx >= 0 {
		pre = v[idx+1:]
		v = v[:idx]
	}
	parts := strings.Split(v, ".")
	core := make([]int, 0, len(parts))
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			n = 0
		}
		core = append(core, n)
	}
	return core, pre
}
