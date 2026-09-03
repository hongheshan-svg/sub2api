package kiro

import "strings"

// maxRefDepth 限制 $ref 展开深度，防止自引用 schema 无限递归。
const maxRefDepth = 16

// maxSchemaNodes 限制清洗后 schema 的总节点数。
//
// 工具 schema 来自客户端，可构造出「每层引用下一层两次」的菱形 $defs：
// 它无环，深度上限不会提前触发，但展开后节点数呈指数增长（深度 16 即约 2^16）。
// 记忆化解决不了这个问题——展开后的输出本身就是指数大小，缓存只避免重复计算。
// 因此必须直接对输出节点总数设上限。
const maxSchemaNodes = 10000

// sanitizeCtx 跟踪 $ref 展开深度与输出节点总数。
type sanitizeCtx struct {
	defs  map[string]any
	nodes int
}

// SanitizeSchema 把 Anthropic 工具的 input_schema 清洗成 Kiro 可接受的形态。
//
// Kiro 对工具 schema 的校验比 Anthropic 严格，不合规会让**整个请求** 400，
// 且换账号重试同样失败（历史上的 "Kiro400" 事故即源于此）。
//
// 规则：
//  1. 展开 $ref（Claude Code 的工具 schema 由 zod/pydantic 生成，普遍带 $ref/$defs）
//  2. 删除所有层级的 additionalProperties
//  3. 删除空的 required 数组
//
// 入参不会被修改，返回新的 map。
func SanitizeSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return map[string]any{}
	}

	defs := collectDefs(schema)
	ctx := &sanitizeCtx{defs: defs, nodes: 0}
	cleaned := sanitizeValue(schema, ctx, 0)

	out, ok := cleaned.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	delete(out, "$defs")
	delete(out, "definitions")
	return out
}

// collectDefs 收集 $defs 与 definitions 下的可引用子 schema。
func collectDefs(schema map[string]any) map[string]any {
	defs := make(map[string]any)
	for _, key := range []string{"$defs", "definitions"} {
		raw, ok := schema[key].(map[string]any)
		if !ok {
			continue
		}
		for name, val := range raw {
			defs[key+"/"+name] = val
		}
	}
	return defs
}

// resolveRef 按 "#/$defs/Name" 形态查表。找不到返回 nil。
func resolveRef(ref string, defs map[string]any) any {
	trimmed := strings.TrimPrefix(ref, "#/")
	if val, ok := defs[trimmed]; ok {
		return val
	}
	return nil
}

// sanitizeValue 递归清洗任意 JSON 值。
func sanitizeValue(value any, ctx *sanitizeCtx, depth int) any {
	switch v := value.(type) {
	case map[string]any:
		// $ref 展开：超过深度上限则丢掉 $ref，退化为宽松对象，
		// 保证自引用 schema 能终止。
		if ref, ok := v["$ref"].(string); ok {
			if depth >= maxRefDepth {
				return map[string]any{"type": "object"}
			}
			if target := resolveRef(ref, ctx.defs); target != nil {
				merged := sanitizeValue(target, ctx, depth+1)
				if mergedMap, ok := merged.(map[string]any); ok {
					// $ref 同级的其他关键字（如 description）保留下来。
					for key, val := range v {
						if key == "$ref" {
							continue
						}
						if _, exists := mergedMap[key]; !exists {
							mergedMap[key] = sanitizeValue(val, ctx, depth+1)
						}
					}
					return mergedMap
				}
				return merged
			}
			// 无法解析的 $ref：退化为宽松对象，好过让上游 400。
			return map[string]any{"type": "object"}
		}

		// 检查节点预算：超过上限则退化为宽松对象，防止菱形 $defs 导致的指数爆炸。
		ctx.nodes++
		if ctx.nodes > maxSchemaNodes {
			return map[string]any{"type": "object"}
		}

		out := make(map[string]any, len(v))
		for key, val := range v {
			switch key {
			case "additionalProperties", "$defs", "definitions", "$schema", "$id":
				continue
			case "required":
				arr, ok := val.([]any)
				if !ok || len(arr) == 0 {
					continue
				}
				// 复制而非别名：out 与入参共享底层数组会让调用方的 schema
				// 被返回值的原地修改污染，违反"入参不会被修改"的约定。
				required := make([]any, len(arr))
				copy(required, arr)
				out[key] = required
				continue
			}
			out[key] = sanitizeValue(val, ctx, depth+1)
		}
		return out

	case []any:
		// 检查节点预算。
		ctx.nodes++
		if ctx.nodes > maxSchemaNodes {
			return map[string]any{"type": "object"}
		}

		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, sanitizeValue(item, ctx, depth+1))
		}
		return out

	default:
		return value
	}
}
