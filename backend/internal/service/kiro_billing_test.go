//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/stretchr/testify/require"
)

// TestKiroUsageMappingKeepsUpstreamCacheTokens 固化设计文档 D4 的计费口径：
// cache token 用 meteringEvent 的真实值，input/output 才是估算。
func TestKiroUsageMappingKeepsUpstreamCacheTokens(t *testing.T) {
	tr := kiro.NewStreamTranslator("claude-sonnet-4.6", "msg_1", false)

	// 直接构造 usage 场景：真实 cache token + 估算 output。
	usage := tr.Usage()
	require.Zero(t, usage.CacheReadInputTokens, "无 meteringEvent 时为 0")

	inbound := &apicompat.AnthropicRequest{
		System: rawJSONForBilling(t, `"a system prompt of some length"`),
		Messages: []apicompat.AnthropicMessage{
			{Role: "user", Content: rawJSONForBilling(t, `"hello there, this is a message"`)},
		},
	}

	claudeUsage := ClaudeUsage{
		InputTokens:              kiro.EstimateRequestInput(inbound),
		OutputTokens:             usage.OutputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		CacheReadInputTokens:     usage.CacheReadInputTokens,
	}

	require.Positive(t, claudeUsage.InputTokens, "input token 必须来自本地估算，不得为 0")
	require.Zero(t, claudeUsage.CacheCreationInputTokens)
}

// TestKiroBillingModeIsToken 固化 usage_log.billing_mode 的取值。
// kiro 走 token 计费（设计文档 D4），credits 只用于调度与额度展示。
func TestKiroBillingModeIsToken(t *testing.T) {
	require.Equal(t, "token", kiroBillingMode,
		"kiro 按估算 token 计费；credits 只记账号层，不进 usage_log")
}

func rawJSONForBilling(t *testing.T, s string) json.RawMessage {
	t.Helper()
	require.True(t, json.Valid([]byte(s)))
	return json.RawMessage(s)
}
