package kiro

import (
	"regexp"
	"strings"
)

// defaultKiroModel 是 Kiro 目前最主力的型号，只用作 DefaultModels() 兜底
// 列表里的一个已知条目（供测试断言"列表非空且包含一个合理默认值"）。
// **不再是 MapModel 的兜底目标**——不支持的模型名现在直接拒绝（ok=false），
// 不会静默换成这个值再假装请求成功。
const defaultKiroModel = "claude-sonnet-4.6"

// kiroModelAliases 把 Anthropic 风格的模型名映射到 Kiro 上游名。
// Kiro 用点号版本号（claude-sonnet-4.6），Anthropic 客户端用连字符。
//
// 故意不含 opus 别名（I5）：Kiro 没有 Opus 型号，之前这里把
// claude-opus-4-5/claude-opus-4-6 静默映射到 claude-sonnet-4.6，客户端请求
// Opus 却拿到 Sonnet 的输出，而计费（usage_log_helpers.go 的
// forwardResultBillingModel）按*请求的*模型名计价，等于按 Opus 价格结算
// Sonnet 的产出——这是一个计费正确性 bug，不是有意的降级策略；SDD ledger
// （.superpowers/sdd/2026-09-03-kiro-platform-phase1/progress.md）里也没有
// 记录任何要求这么做的设计依据。移除后 opus 请求和其它任何不支持的模型名
// 一样被 MapModel 直接拒绝（ok=false），不再假装网关知道该用哪个型号代替
// Opus。
var kiroModelAliases = map[string]string{
	"claude-sonnet-4":   "claude-sonnet-4",
	"claude-sonnet-4-5": "claude-sonnet-4.5",
	"claude-sonnet-4-6": "claude-sonnet-4.6",
	"claude-haiku-4-5":  "claude-haiku-4.5",
}

// dateSuffix 匹配 Anthropic 模型名尾部的日期版本，如 -20250929。
var dateSuffix = regexp.MustCompile(`-\d{8}$`)

// kiroNativeName 匹配已经是 Kiro 形态的名字（版本号带点）。
var kiroNativeName = regexp.MustCompile(`^claude-[a-z]+-\d+\.\d+$`)

// MapModel 把客户端请求的模型名转换为 Kiro 上游可识别的名字。
//
// ok=false 表示这个模型名不受支持——调用方必须拒绝请求，不能静默换成
// defaultKiroModel 再假装成功。真实账号测试发现的教训：之前第 4 条兜底规则
// 对任何未识别的名字（包括明显不属于 Kiro 的模型，如 claude-fable-5-1、
// gpt-4 等）都静默换成 claude-sonnet-4.6 并正常返回——客户端（包括管理端
// "测试连接"功能）永远不会知道自己请求的模型从未被真正服务过，是比 I5
// （opus 别名误判）更广的同类问题：I5 只删了两条错误的别名，没有改变"其余
// 一律兜底"这条规则本身对所有其它不支持模型依然成立。
//
// 规则按优先级：
//  1. 已是 Kiro 形态（claude-xxx-N.M）或 "auto" → 原样透传，ok=true
//     （上游新增型号无需改代码；是否真的存在交给上游判定——
//     decideKiroAction/kiro.Classify 已经能正确处理上游对未知型号的 400）
//  2. 命中别名表 → 映射，ok=true
//  3. 去掉日期后缀后命中别名表 → 映射，ok=true
//  4. 其余（含空输入）→ 不支持，ok=false
func MapModel(requested string) (mapped string, ok bool) {
	name := strings.ToLower(strings.TrimSpace(requested))
	if name == "" {
		return "", false
	}

	if name == "auto" || kiroNativeName.MatchString(name) {
		return name, true
	}

	if m, ok := kiroModelAliases[name]; ok {
		return m, true
	}

	if stripped := dateSuffix.ReplaceAllString(name, ""); stripped != name {
		if m, ok := kiroModelAliases[stripped]; ok {
			return m, true
		}
	}

	return "", false
}

// DefaultModels 返回未从上游拉到模型清单时对外暴露的兜底列表。
func DefaultModels() []string {
	return []string{
		"claude-sonnet-4.6",
		"claude-sonnet-4.5",
		"claude-haiku-4.5",
		"claude-sonnet-4",
	}
}
