package kiro

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestToolNameMapAlreadySafeIsNoOp(t *testing.T) {
	t.Parallel()

	m := NewToolNameMap()
	require.Equal(t, "Read", m.ToKiro("Read"))
	require.Equal(t, "bash", m.ToKiro("bash"))
	require.Equal(t, "camelCaseTool", m.ToKiro("camelCaseTool"))
}

func TestToolNameMapConvertsSnakeAndKebabCase(t *testing.T) {
	t.Parallel()

	// 各自用独立的 map 实例——同一个实例内先后转换 "foo_bar" 与 "foo-bar"
	// 会因为清洗结果相同而触发碰撞消歧（见 TestToolNameMapDisambiguatesCollisions），
	// 这里只关心两者各自单独清洗的结果形状。
	require.Equal(t, "fooBar", NewToolNameMap().ToKiro("foo_bar"))
	require.Equal(t, "fooBar", NewToolNameMap().ToKiro("foo-bar"))
}

func TestToolNameMapConvertsMCPStyleDoubleUnderscoreName(t *testing.T) {
	t.Parallel()

	m := NewToolNameMap()
	got := m.ToKiro("mcp__filesystem__read_file")
	require.NotContains(t, got, "_")
	require.NotContains(t, got, "-")
	require.Regexp(t, `^[A-Za-z][A-Za-z0-9]*$`, got)
}

func TestToolNameMapRoundTripsBack(t *testing.T) {
	t.Parallel()

	m := NewToolNameMap()
	original := "mcp__filesystem__read_file"
	kiroName := m.ToKiro(original)
	require.Equal(t, original, m.ToClient(kiroName))
}

func TestToolNameMapIsStableWithinOneInstance(t *testing.T) {
	t.Parallel()

	m := NewToolNameMap()
	first := m.ToKiro("foo_bar")
	second := m.ToKiro("foo_bar")
	require.Equal(t, first, second)
}

func TestToolNameMapDisambiguatesCollisions(t *testing.T) {
	t.Parallel()

	m := NewToolNameMap()
	a := m.ToKiro("foo_bar")
	b := m.ToKiro("foo-bar")
	require.NotEqual(t, a, b, "两个不同的原始名字清洗后碰撞，必须分别映射到不同的安全名字")
	require.Equal(t, "foo_bar", m.ToClient(a))
	require.Equal(t, "foo-bar", m.ToClient(b))
}

func TestToolNameMapTruncatesOverlongNames(t *testing.T) {
	t.Parallel()

	m := NewToolNameMap()
	long := "mcp__" + strings.Repeat("very_long_server_name_", 5) + "__do_something"
	got := m.ToKiro(long)
	require.LessOrEqual(t, len(got), maxToolNameLength)
	require.Regexp(t, `^[A-Za-z][A-Za-z0-9]*$`, got)
}

func TestToolNameMapUnknownNameRoundTripsUnchanged(t *testing.T) {
	t.Parallel()

	m := NewToolNameMap()
	require.Equal(t, "never_seen", m.ToClient("never_seen"))
}

func TestToolNameMapNilReceiverIsPassthrough(t *testing.T) {
	t.Parallel()

	var m *ToolNameMap
	require.Equal(t, "foo_bar", m.ToKiro("foo_bar"))
	require.Equal(t, "foo_bar", m.ToClient("foo_bar"))
}

func TestToolNameMapEmptyNameIsPassthrough(t *testing.T) {
	t.Parallel()

	m := NewToolNameMap()
	require.Equal(t, "", m.ToKiro(""))
}

func TestToolNameMapPureSymbolNameGetsFallbackPrefix(t *testing.T) {
	t.Parallel()

	m := NewToolNameMap()
	got := m.ToKiro("__---__")
	require.Regexp(t, `^[A-Za-z][A-Za-z0-9]*$`, got)
}

func TestToolNameMapDigitLeadingNameGetsLetterPrefix(t *testing.T) {
	t.Parallel()

	m := NewToolNameMap()
	got := m.ToKiro("123tool")
	require.False(t, got[0] >= '0' && got[0] <= '9')
}
