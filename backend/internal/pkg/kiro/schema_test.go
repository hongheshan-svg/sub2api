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

// TestSanitizeSchemaBroadRefBounded 防御大量并行 $refs 导致的节点爆炸。
// 不同于深度优先的菱形（受 maxRefDepth 限制），宽度优先的并行 refs
// 在浅深度创建大量节点，不受深度上限约束。
func TestSanitizeSchemaBroadRefBounded(t *testing.T) {
	t.Parallel()

	// 构造根节点有多个并行 $refs 的 schema。
	// 每个 $ref 指向一个包含少量节点的子 schema，
	// 但数量足够多（>10000）以超过节点预算。
	//
	// 为避免过大的 JSON，定义一个两层的结构：
	// - BaseNode: 简单的对象，约 10 个节点（包括自己、properties、几个字段）
	// - Root: 1100 个 $refs 到 BaseNode，每个都会被扩展，总共 ~11000 节点
	defs := map[string]any{
		"BaseNode": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"x": map[string]any{"type": "number"},
				"y": map[string]any{"type": "number"},
				"z": map[string]any{"type": "number"},
			},
		},
	}

	// 根的 properties 包含 20000 个 $refs。
	// 每个 $ref 到 BaseNode 时，会创建约 4-5 个节点（BaseNode 本身、properties 等）。
	// 20000 * 5 = 100000 节点，远超 maxSchemaNodes = 10000。
	rootProps := make(map[string]any)
	for i := 0; i < 20000; i++ {
		rootProps[fmt.Sprintf("item%d", i)] = map[string]any{
			"$ref": "#/$defs/BaseNode",
		}
	}

	in := map[string]any{
		"type":       "object",
		"$defs":      defs,
		"properties": rootProps,
	}

	done := make(chan map[string]any, 1)
	go func() { done <- SanitizeSchema(in) }()

	select {
	case out := <-done:
		require.NotNil(t, out)
		// 验证输出有界。节点预算会在 ~10000 处切断，
		// 所以输出应该远小于无预算情况下的完整展开。
		outJSON, _ := json.Marshal(out)
		require.Less(t, len(outJSON), 50000000, "清活后 schema 过大，节点预算可能完全失效")
	case <-timeAfterSeconds(5):
		t.Fatal("SanitizeSchema 在大量并行 $refs 上耗时过长")
	}
}

func timeAfterSeconds(n int) <-chan time.Time {
	return time.After(time.Duration(n) * time.Second)
}
