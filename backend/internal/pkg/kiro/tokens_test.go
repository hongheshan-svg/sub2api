package kiro

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

func TestEstimateTextEmpty(t *testing.T) {
	t.Parallel()
	require.Zero(t, EstimateText(""))
}

func TestEstimateTextShortStrings(t *testing.T) {
	t.Parallel()

	// 长度 < 5 走 ceil(n/3)，下限 1。
	require.Equal(t, 1, EstimateText("a"))
	require.Equal(t, 1, EstimateText("abc"))
	require.Equal(t, 2, EstimateText("abcd"))
}

func TestEstimateTextAsciiProse(t *testing.T) {
	t.Parallel()

	// 36 个普通 ascii 字符（含空格）→ ceil(36/4.5) = 8
	got := EstimateText("the quick brown fox jumps over lazyy")
	require.Equal(t, 8, got)
}

func TestEstimateTextCJKCostsMore(t *testing.T) {
	t.Parallel()

	// 非 ASCII 按 /1.5 计，中文比等长英文贵。
	cjk := EstimateText("中文字符测试内容一二三")
	ascii := EstimateText("aaaaaaaaaaa")
	require.Greater(t, cjk, ascii)
}

func TestEstimateTextSymbolsAndDigits(t *testing.T) {
	t.Parallel()

	// 12 个符号 → ceil(12/1.5) = 8
	require.Equal(t, 8, EstimateText("{}[]()<>!@#$"))
	// 12 个数字 → ceil(12/2) = 6
	require.Equal(t, 6, EstimateText("123456789012"))
}

func TestEstimateTextNeverZeroForNonEmpty(t *testing.T) {
	t.Parallel()
	require.GreaterOrEqual(t, EstimateText(" "), 1)
}

func TestEstimateRequestInputCoversSystemMessagesTools(t *testing.T) {
	t.Parallel()

	base := &apicompat.AnthropicRequest{
		Messages: []apicompat.AnthropicMessage{
			{Role: "user", Content: rawJSON(t, `"hello world this is a message"`)},
		},
	}
	withSystem := &apicompat.AnthropicRequest{
		System:   rawJSON(t, `"a fairly long system prompt goes here"`),
		Messages: base.Messages,
	}
	withTools := &apicompat.AnthropicRequest{
		Messages: base.Messages,
		Tools: []apicompat.AnthropicTool{{
			Name:        "Read",
			Description: "reads a file from disk and returns its contents",
			InputSchema: rawJSON(t, `{"type":"object","properties":{"path":{"type":"string"}}}`),
		}},
	}

	require.Greater(t, EstimateRequestInput(withSystem), EstimateRequestInput(base),
		"system 必须计入 input token")
	require.Greater(t, EstimateRequestInput(withTools), EstimateRequestInput(base),
		"工具声明必须计入 input token")
}

func TestEstimateRequestInputNil(t *testing.T) {
	t.Parallel()
	require.Zero(t, EstimateRequestInput(nil))
}
