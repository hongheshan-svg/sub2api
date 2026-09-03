package kiro

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestClassifyInvalidModelIDIsNetworkNotAccountFault 是红线回归测试。
// 大陆直连 Kiro 必现 INVALID_MODEL_ID；若归类为账号故障，
// 首个请求就会把整个账号池禁掉。
func TestClassifyInvalidModelIDIsNetworkNotAccountFault(t *testing.T) {
	t.Parallel()

	body := []byte(`{"message":"Improperly formed request: INVALID_MODEL_ID"}`)

	// 它通常伴随 400 返回 —— body 检查必须优先于状态码判断。
	got := Classify(400, body)
	require.Equal(t, SignalNetworkRegion, got)
	require.NotEqual(t, SignalBadRequest, got, "不得误判为请求格式错误")

	// 换个状态码也一样。
	require.Equal(t, SignalNetworkRegion, Classify(403, body))

	// 网络/区域问题可以换端点重试，但绝不是账号的错。
	require.True(t, SignalNetworkRegion.Retryable())
	require.False(t, SignalNetworkRegion.Failoverable(),
		"换账号解决不了网络问题，不得触发账号转移")
}

// TestClassifyBadRequestIsNeitherRetryableNorFailoverable 是另一条红线：
// 400 意味着我们自己的请求构造有误，换账号同样失败，重试只会烧光整池。
func TestClassifyBadRequestIsNeitherRetryableNorFailoverable(t *testing.T) {
	t.Parallel()

	got := Classify(400, []byte(`{"message":"Improperly formed request"}`))
	require.Equal(t, SignalBadRequest, got)
	require.False(t, got.Retryable(), "400 重试只会重复失败")
	require.False(t, got.Failoverable(), "400 换账号一样失败，会烧光整池")
}

func TestClassifyAuthAndOverageAndRateLimit(t *testing.T) {
	t.Parallel()

	require.Equal(t, SignalAuthExpired, Classify(401, nil))
	require.Equal(t, SignalAuthExpired, Classify(403, nil))
	require.Equal(t, SignalOverage, Classify(402, nil))
	require.Equal(t, SignalRateLimited, Classify(429, nil))

	require.True(t, SignalAuthExpired.Retryable(), "刷新 token 后应重试一次")
	require.True(t, SignalRateLimited.Retryable(), "先换端点，端点耗尽再交给限流冷却")
	require.True(t, SignalRateLimited.Failoverable())
	require.False(t, SignalOverage.Retryable())
}

func TestClassifySuspensionAndCreditsExhausted(t *testing.T) {
	t.Parallel()

	require.Equal(t, SignalSuspended,
		Classify(403, []byte(`{"message":"Your subscription has been suspended"}`)))
	require.Equal(t, SignalCreditsExhausted,
		Classify(429, []byte(`{"message":"Monthly request limit reached, credits exhausted"}`)))

	require.False(t, SignalSuspended.Retryable())
	require.False(t, SignalSuspended.Failoverable(), "账号被停用，换端点无意义；由上层禁用账号")
	require.True(t, SignalCreditsExhausted.Failoverable(), "额度耗尽应换账号")
}

func TestClassifySuccessAndUnknown(t *testing.T) {
	t.Parallel()

	require.Equal(t, SignalOK, Classify(200, nil))
	require.Equal(t, SignalOK, Classify(204, nil))

	require.Equal(t, SignalUnknown, Classify(500, nil))
	require.True(t, SignalUnknown.Retryable(), "5xx 允许重试")
	require.True(t, SignalUnknown.Failoverable())
}

func TestSignalStringIsStable(t *testing.T) {
	t.Parallel()

	// 这些字符串会进日志与告警，改动会破坏既有检索。
	require.Equal(t, "network_region", SignalNetworkRegion.String())
	require.Equal(t, "bad_request", SignalBadRequest.String())
	require.Equal(t, "credits_exhausted", SignalCreditsExhausted.String())
}
