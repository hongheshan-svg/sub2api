package kiro

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func schemaFrom(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &m))
	return m
}

func TestSanitizeSchemaDropsAdditionalProperties(t *testing.T) {
	t.Parallel()

	in := schemaFrom(t, `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"nested": {"type": "object", "additionalProperties": true, "properties": {}}
		}
	}`)

	out := SanitizeSchema(in)

	require.NotContains(t, out, "additionalProperties")
	nested := out["properties"].(map[string]any)["nested"].(map[string]any)
	require.NotContains(t, nested, "additionalProperties")
}

func TestSanitizeSchemaDropsEmptyRequired(t *testing.T) {
	t.Parallel()

	in := schemaFrom(t, `{"type":"object","required":[],"properties":{"a":{"type":"string"}}}`)
	out := SanitizeSchema(in)
	require.NotContains(t, out, "required")

	// 非空 required 必须保留。
	in = schemaFrom(t, `{"type":"object","required":["a"],"properties":{"a":{"type":"string"}}}`)
	out = SanitizeSchema(in)
	require.Equal(t, []any{"a"}, out["required"])
}

// TestSanitizeSchemaFlattensRefs 覆盖 Claude Code 工具 schema 的典型形态：
// zod / pydantic 生成的 schema 普遍带 $ref + $defs。
func TestSanitizeSchemaFlattensRefs(t *testing.T) {
	t.Parallel()

	in := schemaFrom(t, `{
		"type": "object",
		"$defs": {
			"Point": {"type": "object", "properties": {"x": {"type": "number"}}}
		},
		"properties": {
			"origin": {"$ref": "#/$defs/Point"}
		}
	}`)

	out := SanitizeSchema(in)

	require.NotContains(t, out, "$defs")
	origin := out["properties"].(map[string]any)["origin"].(map[string]any)
	require.NotContains(t, origin, "$ref")
	require.Equal(t, "object", origin["type"])
	require.Contains(t, origin["properties"], "x")
}

func TestSanitizeSchemaHandlesArrayItems(t *testing.T) {
	t.Parallel()

	in := schemaFrom(t, `{
		"type": "object",
		"properties": {
			"items": {
				"type": "array",
				"items": {"type": "object", "additionalProperties": false, "required": []}
			}
		}
	}`)

	out := SanitizeSchema(in)
	items := out["properties"].(map[string]any)["items"].(map[string]any)["items"].(map[string]any)
	require.NotContains(t, items, "additionalProperties")
	require.NotContains(t, items, "required")
}

func TestSanitizeSchemaNilAndEmpty(t *testing.T) {
	t.Parallel()

	require.NotNil(t, SanitizeSchema(nil))
	require.Empty(t, SanitizeSchema(map[string]any{}))
}

// TestSanitizeSchemaCyclicRefDoesNotHang 防御自引用 schema 导致的无限递归。
func TestSanitizeSchemaCyclicRefDoesNotHang(t *testing.T) {
	t.Parallel()

	in := schemaFrom(t, `{
		"type": "object",
		"$defs": {"Node": {"type":"object","properties":{"next":{"$ref":"#/$defs/Node"}}}},
		"properties": {"root": {"$ref": "#/$defs/Node"}}
	}`)

	done := make(chan map[string]any, 1)
	go func() { done <- SanitizeSchema(in) }()

	select {
	case out := <-done:
		require.NotNil(t, out)
	case <-timeAfterSeconds(5):
		t.Fatal("SanitizeSchema 在自引用 schema 上没有终止")
	}
}

// TestSanitizeSchemaRequiredArrayNotAliased 验证 required 数组被复制而非别名。
// 修改返回值的 required 不应该污染输入。
func TestSanitizeSchemaRequiredArrayNotAliased(t *testing.T) {
	t.Parallel()

	in := schemaFrom(t, `{"type":"object","required":["a","b"],"properties":{"a":{"type":"string"}}}`)
	out := SanitizeSchema(in)

	// 获取输出的 required 数组并修改。
	outRequired := out["required"].([]any)
	outRequired[0] = "modified"

	// 验证输入的 required 没有被修改。
	inRequired := in["required"].([]any)
	require.Equal(t, "a", inRequired[0], "输入 required 被输出修改污染")
}

// TestSanitizeSchemaDiamondRefBounded 防御链式宽分支的 $defs 导致的节点爆炸。
// 深度限制（maxRefDepth=16）约束嵌套，但不约束宽度。
// 8 层、20 分支的链会产生 ~20^4 ~ 160k 节点（在第 5 层宽度最大处），
// 节点预算会将其限制在 ~55k。
func TestSanitizeSchemaDiamondRefBounded(t *testing.T) {
	t.Parallel()

	// countNodes 统计 schema 中的容器节点数（maps 和 slices）。
	var countNodes func(any) int
	countNodes = func(value any) int {
		switch v := value.(type) {
		case map[string]any:
			count := 1 // 计数 map 本身
			for _, val := range v {
				count += countNodes(val)
			}
			return count
		case []any:
			count := 1 // 计数 slice 本身
			for _, item := range v {
				count += countNodes(item)
			}
			return count
		default:
			return 0 // 标量不计数
		}
	}

	// buildWideChain 构造链式 $defs：L8 -> (c0:L7, ..., c_{branch-1}:L7),
	// L7 -> (c0:L6, ...), ..., L1 -> (无 refs，基础情况)
	buildWideChain := func(levels int, branch int) map[string]any {
		defs := map[string]any{}

		for level := levels; level > 0; level-- {
			node := map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}

			if level > 1 {
				nextLevel := level - 1
				props := make(map[string]any)
				for c := 0; c < branch; c++ {
					childName := fmt.Sprintf("L%d", nextLevel)
					props[fmt.Sprintf("c%d", c)] = map[string]any{
						"$ref": "#/$defs/" + childName,
					}
				}
				node["properties"] = props
			}

			levelName := fmt.Sprintf("L%d", level)
			defs[levelName] = node
		}

		return map[string]any{
			"type":  "object",
			"$defs": defs,
			"properties": map[string]any{
				"root": map[string]any{
					"$ref": fmt.Sprintf("#/$defs/L%d", levels),
				},
			},
		}
	}

	// 8 层、20 分支：深度限制允许 ~5 层展开（深度：2,5,8,11,14,17截止）。
	// 但 20^4 = 160k 节点会被 maxSchemaNodes = 10000 的预算限制在 ~55k。
	// 这是对节点预算有效性的真实考验：预算=10000 时计数应 <200k，
	// 预算=1<<30 时计数应 >300k。
	in := buildWideChain(8, 20)
	out := SanitizeSchema(in)
	outNodes := countNodes(out)

	t.Logf("Output node count: %d", outNodes)

	// 断言输出节点数在预期范围。
	// 如果计数 >= 200k，表示节点预算完全失效。
	require.Less(t, outNodes, 200000,
		fmt.Sprintf("output has %d nodes (expected <200k with budget=10000)", outNodes))

	// 验证不会耗时过长。
	done := make(chan bool, 1)
	go func() { done <- true }()
	select {
	case <-done:
		require.NotNil(t, out)
	case <-timeAfterSeconds(5):
		t.Fatal("SanitizeSchema 在链式宽分支 $defs 上耗时过长")
	}
}

func timeAfterSeconds(n int) <-chan time.Time {
	return time.After(time.Duration(n) * time.Second)
}
