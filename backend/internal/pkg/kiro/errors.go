package kiro

import (
	"bytes"
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
	// SignalOverage 表示 overage 未开启或已超上限。这是当前账号订阅设置的问题，
	// 不是请求本身的问题——账号池里开了 overage 或还没到上限的其它账号可以
	// 正常服务同一个请求，因此应当换账号（Failoverable）。
	SignalOverage
	// SignalRateLimited 表示该端点额度耗尽，应先换端点。
	SignalRateLimited
	// SignalNetworkRegion 表示网络/区域问题（典型是 INVALID_MODEL_ID）。
	// 这不是账号的错，绝不能据此禁用账号。
	SignalNetworkRegion
	// SignalBadRequest 表示我们自己构造的请求不合法。
	// 不可重试、不可换账号 —— 换了一样失败。
	SignalBadRequest
	// SignalSuspended 表示订阅被停用或 profile 不可用，应禁用账号。禁用账号
	// 和"当前这次请求要不要换账号重试"是两件独立的事——禁用账号防止之后的
	// 请求继续路由到它，但账号池里没被停用的其它账号完全可以正常处理同一个
	// 请求，因此应当换账号（Failoverable），不能因为要禁用当前账号就连带
	// 让当前请求整体失败。
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
// 判断标准是"问题出在当前账号，还是出在请求/网络本身"：
//   - 出在当前账号（额度耗尽、订阅停用、overage 未开、鉴权失效）—— 池子里
//     其它账号大概率没有这个问题，换账号有机会成功，恒为 true。
//   - 出在请求或网络本身（格式错误、区域/网络问题）—— 换哪个账号结果都
//     一样，换账号只会把整池配额烧光，恒为 false。
//
// SignalBadRequest 恒为 false：请求本身不合法，换账号只会把整池配额烧光。
// SignalNetworkRegion 恒为 false：网络问题与账号无关，换账号无济于事。
// SignalSuspended / SignalOverage 恒为 true：这两者都是当前账号的订阅/配置
// 问题，池子里其它账号完全可能没有同样的问题；是否禁用当前账号是另一套
// 独立机制，不能因为要禁用账号就连带让当前请求也失败。
func (s Signal) Failoverable() bool {
	switch s {
	case SignalRateLimited, SignalCreditsExhausted, SignalAuthExpired, SignalUnknown,
		SignalSuspended, SignalOverage:
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
		// 用具体短语而不是裸 "suspend"：裸子串会命中回显文本或助手原文里任何
		// 提到 suspend 的地方，误伤健康账号。
		[]byte("has been suspended"),
		[]byte("account is suspended"),
		[]byte("subscription is suspended"),
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
// 检查顺序是有意为之：
//  1. 2xx 必须最先判定为 SignalOK，绝不允许被 body 内容反过来改判 —— 成功响应
//     的 body 里可能回显了请求文本或助手原文，其中出现敏感词不代表账号出了问题。
//  2. 非 2xx 时，body 特征优先于状态码。INVALID_MODEL_ID 通常伴随 400
//     返回，若先按状态码判成 SignalBadRequest，就会掩盖「这其实是网络问题」这一事实。
func Classify(status int, body []byte) Signal {
	if status >= 200 && status < 300 {
		return SignalOK
	}

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
