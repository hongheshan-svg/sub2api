package kiro

import (
	"bytes"
	"strings"
)

// Signal 是对一次上游响应的语义分类，决定调度侧的动作。
//
// 分类结果通过 Retryable / Failoverable 把「能不能重试、能不能换账号」编码进类型，
// 而不是交给每个调用点自行判断 —— 设计文档 §7.2 记录的两次事故都源于误判。
type Signal int

const (
	// SignalOK 表示成功。
	SignalOK Signal = iota
	// SignalAuthExpired 表示 token 失效，应刷新后重试一次。
	SignalAuthExpired
	// SignalOverage 表示 overage 未开启或已超上限。
	SignalOverage
	// SignalRateLimited 表示该端点额度耗尽，应先换端点。
	SignalRateLimited
	// SignalNetworkRegion 表示网络/区域问题（典型是 INVALID_MODEL_ID）。
	// 这不是账号的错，绝不能据此禁用账号。
	SignalNetworkRegion
	// SignalBadRequest 表示我们自己构造的请求不合法。
	// 不可重试、不可换账号 —— 换了一样失败。
	SignalBadRequest
	// SignalSuspended 表示订阅被停用或 profile 不可用，应禁用账号。
	SignalSuspended
	// SignalCreditsExhausted 表示账号额度耗尽，应冷却并换账号。
	SignalCreditsExhausted
	// SignalUnknown 是兜底（含 5xx）。
	SignalUnknown
)

// String 返回稳定的短名，用于日志与告警检索。改动会破坏既有检索。
func (s Signal) String() string {
	switch s {
	case SignalOK:
		return "ok"
	case SignalAuthExpired:
		return "auth_expired"
	case SignalOverage:
		return "overage"
	case SignalRateLimited:
		return "rate_limited"
	case SignalNetworkRegion:
		return "network_region"
	case SignalBadRequest:
		return "bad_request"
	case SignalSuspended:
		return "suspended"
	case SignalCreditsExhausted:
		return "credits_exhausted"
	default:
		return "unknown"
	}
}

// Retryable 表示是否值得就当前账号再试一次（可能换端点）。
func (s Signal) Retryable() bool {
	switch s {
	case SignalAuthExpired, SignalRateLimited, SignalNetworkRegion, SignalUnknown:
		return true
	default:
		return false
	}
}

// Failoverable 表示是否应该换一个账号重试。
//
// SignalBadRequest 恒为 false：请求本身不合法，换账号只会把整池配额烧光。
// SignalNetworkRegion 恒为 false：网络问题与账号无关，换账号无济于事。
func (s Signal) Failoverable() bool {
	switch s {
	case SignalRateLimited, SignalCreditsExhausted, SignalAuthExpired, SignalUnknown:
		return true
	default:
		return false
	}
}

// 错误 body 中的特征串。全部小写比较。
var (
	invalidModelIDMarkers = [][]byte{
		[]byte("invalid_model_id"),
		[]byte("invalid model id"),
	}
	suspensionMarkers = [][]byte{
		[]byte("suspend"),
		[]byte("account is disabled"),
		[]byte("profile is not available"),
		[]byte("profilearn is not available"),
	}
	creditsExhaustedMarkers = [][]byte{
		[]byte("credits exhausted"),
		[]byte("insufficient credits"),
		[]byte("not enough credits"),
		[]byte("monthly request limit"),
		[]byte("usage limit reached"),
	}
)

func containsAny(haystack []byte, needles [][]byte) bool {
	for _, n := range needles {
		if bytes.Contains(haystack, n) {
			return true
		}
	}
	return false
}

// Classify 把一次上游响应归类。
//
// 检查顺序是有意为之：body 特征优先于状态码。INVALID_MODEL_ID 通常伴随 400
// 返回，若先按状态码判成 SignalBadRequest，就会掩盖「这其实是网络问题」这一事实。
func Classify(status int, body []byte) Signal {
	lower := bytes.ToLower(bytes.TrimSpace(body))

	if len(lower) > 0 {
		// 网络/区域问题必须最先识别 —— 它伪装成 400。
		if containsAny(lower, invalidModelIDMarkers) {
			return SignalNetworkRegion
		}
		if containsAny(lower, creditsExhaustedMarkers) {
			return SignalCreditsExhausted
		}
		if containsAny(lower, suspensionMarkers) {
			return SignalSuspended
		}
	}

	switch {
	case status >= 200 && status < 300:
		return SignalOK
	case status == 401 || status == 403:
		return SignalAuthExpired
	case status == 402:
		return SignalOverage
	case status == 429:
		return SignalRateLimited
	case status == 400:
		return SignalBadRequest
	default:
		return SignalUnknown
	}
}

// IsBadRequestBody 供调用方在记录诊断日志时判断是否需要打印请求摘要。
// 400 是我们自己的构造错误，日志里必须留下足以定位的请求形状。
func IsBadRequestBody(body []byte) bool {
	return strings.Contains(strings.ToLower(string(body)), "improperly formed request")
}
