package kiro

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMapModelKnownAliases(t *testing.T) {
	t.Parallel()

	require.Equal(t, "claude-sonnet-4.6", MapModel("claude-sonnet-4-6"))
	require.Equal(t, "claude-sonnet-4.5", MapModel("claude-sonnet-4-5"))
	require.Equal(t, "claude-haiku-4.5", MapModel("claude-haiku-4-5"))
}

func TestMapModelStripsDateSuffix(t *testing.T) {
	t.Parallel()

	require.Equal(t, "claude-sonnet-4.5", MapModel("claude-sonnet-4-5-20250929"))
	require.Equal(t, "claude-haiku-4.5", MapModel("claude-haiku-4-5-20251001"))
}

func TestMapModelPassesThroughKiroNativeNames(t *testing.T) {
	t.Parallel()

	// 已经是 Kiro 形态（点号版本号）的直接透传，
	// 这样上游新增型号无需改代码。
	require.Equal(t, "claude-sonnet-4.6", MapModel("claude-sonnet-4.6"))
	require.Equal(t, "claude-sonnet-9.9", MapModel("claude-sonnet-9.9"))
	require.Equal(t, "auto", MapModel("auto"))
}

func TestMapModelUnknownFallsBackToDefault(t *testing.T) {
	t.Parallel()

	require.Equal(t, defaultKiroModel, MapModel("gpt-4o"))
	require.Equal(t, defaultKiroModel, MapModel(""))
}

func TestMapModelIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	require.Equal(t, "claude-sonnet-4.6", MapModel("Claude-Sonnet-4-6"))
}

// TestMapModelOpusRequestsFallThroughToDefaultModel 是 I5 的回归：Kiro 没有
// Opus 型号，claude-opus-4-5 / claude-opus-4-6 之前被静默映射到
// claude-sonnet-4.6——客户端请求 Opus 却拿到 Sonnet 输出，而计费按*请求的*
// 模型名计价（forwardResultBillingModel），等于按 Opus 价格结算 Sonnet 的
// 产出，是计费正确性 bug，不是有意的降级策略（SDD ledger 没有记录任何
// 支持这么做的设计依据）。移除别名之后，opus 请求必须和其它任何未识别的
// 模型名一样，落到 defaultKiroModel 兜底——不再有 opus 专属的特殊映射。
func TestMapModelOpusRequestsFallThroughToDefaultModel(t *testing.T) {
	t.Parallel()

	require.Equal(t, defaultKiroModel, MapModel("claude-opus-4-5"))
	require.Equal(t, defaultKiroModel, MapModel("claude-opus-4-6"))
	// 带日期后缀的 Opus 请求同样必须落到兜底——之前的别名表在这条路径上
	// 也生效（MapModel 会先剥离日期后缀再查表），必须确认剥离逻辑本身
	// 没有导致这条路径绕过 I5 的修复。
	require.Equal(t, defaultKiroModel, MapModel("claude-opus-4-5-20250929"))
}

func TestDefaultModelsNonEmptyAndContainsDefault(t *testing.T) {
	t.Parallel()

	models := DefaultModels()
	require.NotEmpty(t, models)
	require.Contains(t, models, defaultKiroModel)
}
