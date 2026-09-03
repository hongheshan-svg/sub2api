package kiro

import (
	"regexp"
	"strings"
)

// defaultKiroModel 是未知模型名的兜底目标。
const defaultKiroModel = "claude-sonnet-4.6"

// kiroModelAliases 把 Anthropic 风格的模型名映射到 Kiro 上游名。
// Kiro 用点号版本号（claude-sonnet-4.6），Anthropic 客户端用连字符。
var kiroModelAliases = map[string]string{
	"claude-sonnet-4":   "claude-sonnet-4",
	"claude-sonnet-4-5": "claude-sonnet-4.5",
	"claude-sonnet-4-6": "claude-sonnet-4.6",
	"claude-haiku-4-5":  "claude-haiku-4.5",
	"claude-opus-4-5":   "claude-sonnet-4.6",
	"claude-opus-4-6":   "claude-sonnet-4.6",
}

// dateSuffix 匹配 Anthropic 模型名尾部的日期版本，如 -20250929。
var dateSuffix = regexp.MustCompile(`-\d{8}$`)

// kiroNativeName 匹配已经是 Kiro 形态的名字（版本号带点）。
var kiroNativeName = regexp.MustCompile(`^claude-[a-z]+-\d+\.\d+$`)

// MapModel 把客户端请求的模型名转换为 Kiro 上游可识别的名字。
//
// 规则按优先级：
//  1. 已是 Kiro 形态（claude-xxx-N.M）或 "auto" → 原样透传（上游新增型号无需改代码）
//  2. 命中别名表 → 映射
//  3. 去掉日期后缀后命中别名表 → 映射
//  4. 其余 → 兜底到 defaultKiroModel
func MapModel(requested string) string {
	name := strings.ToLower(strings.TrimSpace(requested))
	if name == "" {
		return defaultKiroModel
	}

	if name == "auto" || kiroNativeName.MatchString(name) {
		return name
	}

	if mapped, ok := kiroModelAliases[name]; ok {
		return mapped
	}

	if stripped := dateSuffix.ReplaceAllString(name, ""); stripped != name {
		if mapped, ok := kiroModelAliases[stripped]; ok {
			return mapped
		}
	}

	return defaultKiroModel
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
