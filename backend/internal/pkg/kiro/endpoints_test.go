package kiro

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEndpointsForOAuthAccountHasThreeFallbacks(t *testing.T) {
	t.Parallel()

	eps := EndpointsFor(false, "us-east-1")
	require.Len(t, eps, 3, "OAuth 账号有三个可回退端点")

	require.Equal(t, "Kiro IDE", eps[0].Name)
	require.Empty(t, eps[0].AmzTarget, "首选端点不带 x-amz-target")

	require.Contains(t, eps[1].URL, "codewhisperer.")
	require.Equal(t, "AmazonCodeWhispererStreamingService.GenerateAssistantResponse", eps[1].AmzTarget)

	require.Equal(t, "AmazonQDeveloperStreamingService.SendMessage", eps[2].AmzTarget)

	for _, ep := range eps {
		require.Equal(t, "AI_EDITOR", ep.Origin)
		require.True(t, strings.HasSuffix(ep.URL, "/generateAssistantResponse"))
	}
}

// TestEndpointsForAPIKeyUsesCLIRuntimeOnly 覆盖 API Key 账号的独立路径：
// 走 runtime.{region}.kiro.dev，origin 是 KIRO_CLI，且不带 profileArn（由调用方保证）。
func TestEndpointsForAPIKeyUsesCLIRuntimeOnly(t *testing.T) {
	t.Parallel()

	eps := EndpointsFor(true, "us-east-1")
	require.Len(t, eps, 1)
	require.Equal(t, "Kiro CLI", eps[0].Name)
	require.Equal(t, "KIRO_CLI", eps[0].Origin)
	require.Contains(t, eps[0].URL, "runtime.us-east-1.kiro.dev")
}

func TestEndpointsForRegionalization(t *testing.T) {
	t.Parallel()

	eps := EndpointsFor(false, "eu-central-1")
	require.Contains(t, eps[0].URL, "q.eu-central-1.amazonaws.com")

	// 空 region 退回默认。
	eps = EndpointsFor(false, "")
	require.Contains(t, eps[0].URL, "q."+defaultRegion+".amazonaws.com")
}
