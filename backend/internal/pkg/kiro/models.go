package kiro

import (
	"regexp"
	"strings"
)

// defaultKiroModel 是 Kiro 目前最主力的型号，只用作 DefaultModels() 兜底
// 列表里的一个已知条目（供测试断言"列表非空且包含一个合理默认值"）。
// **不是 MapModel 的兜底目标**——不支持的模型名直接拒绝（ok=false），
// 不会静默换成这个值再假装请求成功。
const defaultKiroModel = "claude-sonnet-4.6"

// kiroModelAliases 把 Anthropic 风格的模型名映射到 Kiro 上游名。
// Kiro 大多数型号用点号版本号（claude-sonnet-4.6），Anthropic 客户端用
// 连字符；但 Kiro 2026-07-25 上线的几个新型号（opus-5/sonnet-5/fable-5）
// 命名里根本没有次版本号，原样就是目标值，别名表里体现为恒等映射。
//
// I5 的原判断"Kiro 没有 Opus 型号"是错的——真实账号测试证实 claude-opus-5
// 确实被 Kiro 支持（此前因为不在别名表、也不匹配 kiroNativeName 的点号
// 形态，被本文件的兜底逻辑直接拒绝）。核实过一个活跃维护的第三方 Kiro
// 代理实现（含 2026-07-25 添加 opus-5 支持时的真实上游抓包，
// modelId/modelName 均为 "claude-opus-5"，rateMultiplier 2.2、
// maxOutputTokens 128000，与 opus-4.7/4.8 同档）确认 Kiro 实际支持完整的
// opus-4.5/4.6/4.7/4.8/5 家族，只是原来的别名表从未收录——I5 当初删掉的
// 两条（opus-4-5/4-6 错误指向 sonnet-4.6）方向是对的，但没有补上正确的
// 目标（opus-4.5/opus-4.6 本身），这次一并补齐整个 opus 家族 + sonnet-5 +
// fable，而不是只改 I5 报告的那两条。
//
// claude-fable-5-1 与 claude-fable-5 是本仓库 claude.DefaultModels 里并存
// 的两个 Anthropic 侧模型 ID（分别对应正式版/预览版），但 Kiro 侧只有一个
// 不分次版本号的 "claude-fable-5"，两者都映射到它。
var kiroModelAliases = map[string]string{
	"claude-sonnet-4":   "claude-sonnet-4",
	"claude-sonnet-4-5": "claude-sonnet-4.5",
	"claude-sonnet-4-6": "claude-sonnet-4.6",
	"claude-sonnet-5":   "claude-sonnet-5",
	"claude-haiku-4-5":  "claude-haiku-4.5",
	"claude-opus-4-5":   "claude-opus-4.5",
	"claude-opus-4-6":   "claude-opus-4.6",
	"claude-opus-4-7":   "claude-opus-4.7",
	"claude-opus-4-8":   "claude-opus-4.8",
	"claude-opus-5":     "claude-opus-5",
	"claude-fable-5":    "claude-fable-5",
	"claude-fable-5-1":  "claude-fable-5",
}

// dateSuffix 匹配 Anthropic 模型名尾部的日期版本，如 -20250929。
var dateSuffix = regexp.MustCompile(`-\d{8}$`)

// kiroNativeName 匹配已经是 Kiro 形态的名字（版本号带点）。
var kiroNativeName = regexp.MustCompile(`^claude-[a-z]+-\d+\.\d+$`)

// MapModel 把客户端请求的模型名转换为 Kiro 上游可识别的名字。
//
// 设计对齐 AntigravityGatewayService 的既有约定（domain.
// DefaultAntigravityModelMapping + Account.GetMappedModel）：本地维护一份
// 尽量准确、可维护的白名单，命中就映射，未命中就干净拒绝——不是"猜一个
// 默认模型硬答应"，也不是"不管三七二十一转发给上游让它兜底"。这个仓库里
// 已经有稳定先例（Antigravity/Grok 都是这个模式），Kiro 没有理由另起一条
// 不同的路。
//
// kiroModelAliases 白名单本身两次被证明不完整/错误过，但错误不在"要不要
// 维护白名单"，而在白名单内容本身：第一次是把不认识的名字静默换成
// sonnet-4.6 再假装成功；第二次是白名单确实漏收了 Kiro 实际支持的
// claude-opus-5 一整个家族。这次已经用真实账号测试 + 第三方 Kiro 实现的
// 真实上游抓包核实过，把 opus-4.5/4.6/4.7/4.8/5、sonnet-5、fable 全部补
// 齐——修的是白名单的准确性，不是丢掉白名单这个机制本身。
//
// 规则按优先级：
//  1. 空输入 → ok=false
//  2. "auto" 或已是 Kiro 点号形态（claude-xxx-N.M）→ 原样透传，ok=true
//     （上游新增型号无需改代码；是否真的存在交给上游判定）
//  3. 命中别名表 → 映射，ok=true
//  4. 去掉日期后缀后命中别名表 → 映射，ok=true
//  5. 其余 → 不支持，ok=false（调用方必须拒绝，不能静默换模型或裸转发）
func MapModel(requested string) (mapped string, ok bool) {
	name := strings.ToLower(strings.TrimSpace(requested))
	if name == "" {
		return "", false
	}

	if name == "auto" || kiroNativeName.MatchString(name) {
		return name, true
	}

	if m, found := kiroModelAliases[name]; found {
		return m, true
	}

	if stripped := dateSuffix.ReplaceAllString(name, ""); stripped != name {
		if m, found := kiroModelAliases[stripped]; found {
			return m, true
		}
	}

	return "", false
}

// DefaultModels 返回未从上游拉到模型清单时对外暴露的兜底列表——覆盖
// kiroModelAliases 里全部已确认受支持的目标型号（保持与其同步，新增别名
// 时这里也要加，否则 /v1/models 兜底列表会缺新模型）。
func DefaultModels() []string {
	return []string{
		"claude-sonnet-4.6",
		"claude-sonnet-4.5",
		"claude-sonnet-4",
		"claude-sonnet-5",
		"claude-haiku-4.5",
		"claude-opus-4.5",
		"claude-opus-4.6",
		"claude-opus-4.7",
		"claude-opus-4.8",
		"claude-opus-5",
		"claude-fable-5",
	}
}
