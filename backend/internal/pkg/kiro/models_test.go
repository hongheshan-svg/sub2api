package kiro

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMapModelKnownAliases(t *testing.T) {
	t.Parallel()

	mapped, ok := MapModel("claude-sonnet-4-6")
	require.True(t, ok)
	require.Equal(t, "claude-sonnet-4.6", mapped)

	mapped, ok = MapModel("claude-sonnet-4-5")
	require.True(t, ok)
	require.Equal(t, "claude-sonnet-4.5", mapped)

	mapped, ok = MapModel("claude-haiku-4-5")
	require.True(t, ok)
	require.Equal(t, "claude-haiku-4.5", mapped)
}

func TestMapModelStripsDateSuffix(t *testing.T) {
	t.Parallel()

	mapped, ok := MapModel("claude-sonnet-4-5-20250929")
	require.True(t, ok)
	require.Equal(t, "claude-sonnet-4.5", mapped)

	mapped, ok = MapModel("claude-haiku-4-5-20251001")
	require.True(t, ok)
	require.Equal(t, "claude-haiku-4.5", mapped)
}

func TestMapModelPassesThroughKiroNativeNames(t *testing.T) {
	t.Parallel()

	// 已经是 Kiro 形态（点号版本号）的直接透传，ok=true——
	// 这样上游新增型号无需改代码，是否真的存在交给上游判定。
	mapped, ok := MapModel("claude-sonnet-4.6")
	require.True(t, ok)
	require.Equal(t, "claude-sonnet-4.6", mapped)

	mapped, ok = MapModel("claude-sonnet-9.9")
	require.True(t, ok)
	require.Equal(t, "claude-sonnet-9.9", mapped)

	mapped, ok = MapModel("auto")
	require.True(t, ok)
	require.Equal(t, "auto", mapped)
}

// TestMapModelUnknownIsRejected 是真实账号测试发现的回归：不属于 Kiro 的
// 模型名（既不是 Kiro 原生点号形态，也不在别名表里）必须返回 ok=false，
// 调用方据此拒绝请求——不能像之前那样静默换成 defaultKiroModel 再假装
// 请求成功了。这正是"管理端测试连接不管选什么模型都显示完成"这个用户
// 报告的症状的根因：MapModel 从不报告"这个模型我不认识"。
func TestMapModelUnknownIsRejected(t *testing.T) {
	t.Parallel()

	_, ok := MapModel("gpt-4o")
	require.False(t, ok)

	_, ok = MapModel("")
	require.False(t, ok)

	// claude-fable-5-1 是真实场景里触发这个 bug 的具体例子：Kiro 完全不
	// 支持 Fable，连字符形态既不匹配 kiroNativeName（要求点号版本号）
	// 也不在别名表里。
	_, ok = MapModel("claude-fable-5-1")
	require.False(t, ok)
}

func TestMapModelIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	mapped, ok := MapModel("Claude-Sonnet-4-6")
	require.True(t, ok)
	require.Equal(t, "claude-sonnet-4.6", mapped)
}

// TestMapModelOpusRequestsAreRejected 是 I5 的回归，在 MapModel 改成
// (mapped, ok) 之后更新：Kiro 没有 Opus 型号，claude-opus-4-5 /
// claude-opus-4-6 之前被静默映射到 claude-sonnet-4.6——客户端请求 Opus
// 却拿到 Sonnet 输出，而计费按*请求的*模型名计价
// （forwardResultBillingModel），等于按 Opus 价格结算 Sonnet 的产出，是
// 计费正确性 bug。别名已经删除；现在的正确行为不是"落到某个默认模型"，
// 而是和其它任何不支持的模型一样被拒绝（ok=false）。
func TestMapModelOpusRequestsAreRejected(t *testing.T) {
	t.Parallel()

	_, ok := MapModel("claude-opus-4-5")
	require.False(t, ok)

	_, ok = MapModel("claude-opus-4-6")
	require.False(t, ok)

	// 带日期后缀的 Opus 请求同样必须被拒绝——之前的别名表在这条路径上
	// 也生效（MapModel 会先剥离日期后缀再查表），必须确认剥离逻辑本身
	// 没有导致这条路径绕过拒绝。
	_, ok = MapModel("claude-opus-4-5-20250929")
	require.False(t, ok)
}

func TestDefaultModelsNonEmptyAndContainsDefault(t *testing.T) {
	t.Parallel()

	models := DefaultModels()
	require.NotEmpty(t, models)
	require.Contains(t, models, defaultKiroModel)
}
