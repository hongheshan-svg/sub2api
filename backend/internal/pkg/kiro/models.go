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
// 连字符。
//
// I5 的原判断"Kiro 没有 Opus 型号"是错的——真实账号测试证实 claude-opus-5
// 确实被 Kiro 支持（此前因为不在别名表、也不匹配 kiroNativeName 的点号
// 形态，被本文件的兜底逻辑直接拒绝），Kiro 用不分次版本号的
// "claude-opus-5" 原样服务它，已用真实账号直接测试确认（响应正常）。
//
// 一次教训：曾经参考一个第三方 Kiro 代理实现的 map_model 表，一并加过
// opus-4.5/4.6/4.7/4.8、sonnet-5、fable-5/fable-5-1，其中 fable 那两条
// 被真实账号测试证伪——Kiro 对 "claude-fable-5" 回真实 400
// INVALID_MODEL_ID（"Invalid model. Please select a different model to
// continue."），说明该参考实现对这几个较新型号的命名并不完全可靠。既然
// 同一个来源在 fable 上出过错，其余几条（opus-4.5/4.6/4.7/4.8、sonnet-5）
// 同样只是未经证实的猜测，不能因为"格式看起来合理"就当真——已经跟
// claude-opus-5 一样被同一来源"猜对过一次"不代表这个来源整体可信。已经
// 移除，只保留真正经过验证的条目。
//
// 白名单机制本身没有问题（架构对齐 AntigravityGatewayService 的
// DefaultAntigravityModelMapping，见 MapModel 文档）——出错的是内容来源
// 不够可靠。以后新增条目前，先用真实账号测试连接直接验证候选模型名（点号
// 原生形态可以不经别名表直接透传，见 MapModel 规则 2），拿到 200 或明确的
// 非 INVALID_MODEL_ID 错误后再收录进别名表，不要只凭第三方参考实现下结论。
var kiroModelAliases = map[string]string{
	"claude-sonnet-4":   "claude-sonnet-4",
	"claude-sonnet-4-5": "claude-sonnet-4.5",
	"claude-sonnet-4-6": "claude-sonnet-4.6",
	"claude-haiku-4-5":  "claude-haiku-4.5",
	"claude-opus-5":     "claude-opus-5",
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
// kiroModelAliases 白名单内容出过错（第一次是把不认识的名字静默换成
// sonnet-4.6 再假装成功；第二次是漏收了 Kiro 实际支持的 claude-opus-5；
// 第三次是信了一个第三方参考实现里未经验证就加了几条、其中 fable 那两条
// 被真实测试证伪——见上面的详细说明），但错误从未出在"要不要维护白
// 名单"这个机制本身。每一条都只应该在真实验证（真实账号测试连接得到
// 200，或已是长期批量验证过的既有条目）之后才收录。
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
		"claude-haiku-4.5",
		"claude-opus-5",
	}
}
