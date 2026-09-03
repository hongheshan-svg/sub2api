package kiro

import (
	"encoding/json"
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

// TestSanitizeSchemaDiamondRefBounded 防御菱形 $defs 导致的指数爆炸。
// 每层引用下一层两次会导致节点数呈指数增长。
func TestSanitizeSchemaDiamondRefBounded(t *testing.T) {
	t.Parallel()

	// 构造菱形 $defs：L0 -> (L1, L1), L1 -> (L2, L2), ...
	// 深度 16 无预算限制会产生 ~2^16 = 65536 节点，深度 20 会更糟。
	buildDiamond := func(depth int) map[string]any {
		defs := map[string]any{}
		for i := depth; i > 0; i-- {
			node := map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}
			if i > 1 {
				// 每层引用下一层两次。
				nextRef := "#/$defs/L" + string(rune('0'+i-1))
				node["properties"] = map[string]any{
					"left":  map[string]any{"$ref": nextRef},
					"right": map[string]any{"$ref": nextRef},
				}
			}
			defs["L"+string(rune('0'+i))] = node
		}

		return map[string]any{
			"type":  "object",
			"$defs": defs,
			"properties": map[string]any{
				"root": map[string]any{"$ref": "#/$defs/L" + string(rune('0'+depth))},
			},
		}
	}

	in := buildDiamond(16)

	done := make(chan map[string]any, 1)
	go func() { done <- SanitizeSchema(in) }()

	select {
	case out := <-done:
		require.NotNil(t, out)
		// 验证输出是有界的（不是指数爆炸）。
		// 如果没有节点预算，这会悬挂或产生巨大结构。
		outJSON, _ := json.Marshal(out)
		require.Less(t, len(outJSON), 100000, "清洗后 schema 过大，可能未有效限制节点数")
	case <-timeAfterSeconds(5):
		t.Fatal("SanitizeSchema 在菱形 $defs 上没有终止或耗时过长")
	}
}

func timeAfterSeconds(n int) <-chan time.Time {
	return time.After(time.Duration(n) * time.Second)
}
