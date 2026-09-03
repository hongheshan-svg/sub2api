package kiro

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMapModelKnownAliases(t *testing.T) {
	t.Parallel()

	require.Equal(t, "claude-sonnet-4.6", MapModel("claude-sonnet-4-6"))
	require.Equal(t, "claude-sonnet-4.5", MapModel("claude-sonnet-4-5"))
	require.Equal(t, "claude-haiku-4.5", MapModel("claude-haiku-4-5"))
}

func TestMapModelStripsDateSuffix(t *testing.T) {
	t.Parallel()

	require.Equal(t, "claude-sonnet-4.5", MapModel("claude-sonnet-4-5-20250929"))
	require.Equal(t, "claude-haiku-4.5", MapModel("claude-haiku-4-5-20251001"))
}

func TestMapModelPassesThroughKiroNativeNames(t *testing.T) {
	t.Parallel()

	// 已经是 Kiro 形态（点号版本号）的直接透传，
	// 这样上游新增型号无需改代码。
	require.Equal(t, "claude-sonnet-4.6", MapModel("claude-sonnet-4.6"))
	require.Equal(t, "claude-sonnet-9.9", MapModel("claude-sonnet-9.9"))
	require.Equal(t, "auto", MapModel("auto"))
}

func TestMapModelUnknownFallsBackToDefault(t *testing.T) {
	t.Parallel()

	require.Equal(t, defaultKiroModel, MapModel("gpt-4o"))
	require.Equal(t, defaultKiroModel, MapModel(""))
}

func TestMapModelIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	require.Equal(t, "claude-sonnet-4.6", MapModel("Claude-Sonnet-4-6"))
}

func TestDefaultModelsNonEmptyAndContainsDefault(t *testing.T) {
	t.Parallel()

	models := DefaultModels()
	require.NotEmpty(t, models)
	require.Contains(t, models, defaultKiroModel)
}
