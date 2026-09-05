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

	// claude-opus-4-5 的 Anthropic 侧规范 ID 带日期戳（见 claude.DefaultModels
	// 与 claude.ModelIDOverrides），必须走同一条剥离日期后缀再查表的路径。
	mapped, ok = MapModel("claude-opus-4-5-20251101")
	require.True(t, ok)
	require.Equal(t, "claude-opus-4.5", mapped)
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
// 报告的症状的根因：MapModel 从不报告"这个模型我不认识"。设计参照
// AntigravityGatewayService 的既有约定（一份准确的白名单，命中就映射、
// 未命中就干净拒绝），不是转发给上游让它兜底。
func TestMapModelUnknownIsRejected(t *testing.T) {
	t.Parallel()

	_, ok := MapModel("gpt-4o")
	require.False(t, ok)

	_, ok = MapModel("")
	require.False(t, ok)

	// claude-fable-5-2（假设的未来次版本号）目前既不在别名表，也不是
	// Kiro 原生点号形态，必须被拒绝——与已确认支持的 claude-fable-5/
	// claude-fable-5-1（见 TestMapModelFableAliases）区分开。
	_, ok = MapModel("claude-fable-5-2")
	require.False(t, ok)
}

func TestMapModelIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	mapped, ok := MapModel("Claude-Sonnet-4-6")
	require.True(t, ok)
	require.Equal(t, "claude-sonnet-4.6", mapped)
}

// TestMapModelOpusFamilyIsSupported 是用户真实账号测试发现的回归：
// I5（整分支最终评审）判定"Kiro 没有 Opus 型号"并删除了错误的
// opus-4-5/opus-4-6→sonnet-4.6 别名，方向是对的（不能悄悄把 Opus 请求
// 换成 Sonnet 输出），但结论错了——Kiro 实际支持完整的 opus-4.5/4.6/
// 4.7/4.8/5 家族，只是原来的别名表从未收录正确的目标。用户选择
// claude-opus-5 测试连接时被错误拒绝，核实一个活跃维护的第三方 Kiro
// 代理实现（含真实上游抓包）确认后补齐整个家族。
func TestMapModelOpusFamilyIsSupported(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"claude-opus-4-5": "claude-opus-4.5",
		"claude-opus-4-6": "claude-opus-4.6",
		"claude-opus-4-7": "claude-opus-4.7",
		"claude-opus-4-8": "claude-opus-4.8",
		"claude-opus-5":   "claude-opus-5",
	}
	for requested, want := range cases {
		mapped, ok := MapModel(requested)
		require.True(t, ok, "claude-opus 家族的 %s 必须被识别为受支持", requested)
		require.Equal(t, want, mapped)
	}
}

// TestMapModelSonnet5IsSupported 覆盖 claude-sonnet-5（与 opus-5 同批
// 2026-07-25 上线，同样是无次版本号的原样透传目标）。
func TestMapModelSonnet5IsSupported(t *testing.T) {
	t.Parallel()

	mapped, ok := MapModel("claude-sonnet-5")
	require.True(t, ok)
	require.Equal(t, "claude-sonnet-5", mapped)
}

// TestMapModelFableAliases 覆盖 claude.DefaultModels 里并存的两个 Fable
// Anthropic 侧 ID（claude-fable-5-1 正式版、claude-fable-5 预览版）都必须
// 映射到 Kiro 侧唯一的、不分次版本号的 "claude-fable-5"。
func TestMapModelFableAliases(t *testing.T) {
	t.Parallel()

	mapped, ok := MapModel("claude-fable-5-1")
	require.True(t, ok)
	require.Equal(t, "claude-fable-5", mapped)

	mapped, ok = MapModel("claude-fable-5")
	require.True(t, ok)
	require.Equal(t, "claude-fable-5", mapped)
}

func TestDefaultModelsNonEmptyAndContainsDefault(t *testing.T) {
	t.Parallel()

	models := DefaultModels()
	require.NotEmpty(t, models)
	require.Contains(t, models, defaultKiroModel)
}

// TestDefaultModelsStaysInSyncWithAliasTargets 是防漂移守卫：每次给
// kiroModelAliases 加新别名，DefaultModels() 必须同步收录其映射目标，
// 否则 /v1/models 与管理端候选列表会缺新模型（I4 的场景）。
func TestDefaultModelsStaysInSyncWithAliasTargets(t *testing.T) {
	t.Parallel()

	defaults := DefaultModels()
	defaultSet := make(map[string]bool, len(defaults))
	for _, m := range defaults {
		defaultSet[m] = true
	}

	seen := make(map[string]bool)
	for _, target := range kiroModelAliases {
		if seen[target] {
			continue
		}
		seen[target] = true
		require.True(t, defaultSet[target],
			"kiroModelAliases 的映射目标 %q 必须出现在 DefaultModels() 里", target)
	}
}
