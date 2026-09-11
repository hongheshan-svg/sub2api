package kiro

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListModelsHostFor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		region string
		want   string
	}{
		{name: "empty region defaults to us-east-1", region: "", want: "https://management.us-east-1.kiro.dev"},
		{name: "explicit region lowercased", region: "EU-Central-1", want: "https://management.eu-central-1.kiro.dev"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, ListModelsHostFor(tc.region))
		})
	}
}

func TestBuildListModelsURL(t *testing.T) {
	t.Parallel()
	require.Equal(t, "https://management.us-east-1.kiro.dev/", BuildListModelsURL(""))
	require.Equal(t, "https://management.eu-central-1.kiro.dev/", BuildListModelsURL("eu-central-1"))
}

func TestBuildListModelsRequestBody(t *testing.T) {
	t.Parallel()

	body, err := BuildListModelsRequestBody("arn:aws:codewhisperer:us-east-1:123456789012:profile/abcdef123456")
	require.NoError(t, err)
	require.JSONEq(t, `{"profileArn":"arn:aws:codewhisperer:us-east-1:123456789012:profile/abcdef123456"}`, string(body))

	emptyBody, err := BuildListModelsRequestBody("  ")
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(emptyBody))
}

func TestParseListModelsResponsePrefersPrimaryFieldNames(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"models":[
		{"modelId":"claude-sonnet-4.6","modelName":"Claude Sonnet 4.6"},
		{"modelId":"claude-opus-5"}
	]}`)
	models, err := ParseListModelsResponse(raw)
	require.NoError(t, err)
	require.Equal(t, []ModelInfo{
		{ID: "claude-sonnet-4.6", Name: "Claude Sonnet 4.6"},
		{ID: "claude-opus-5", Name: ""},
	}, models)
}

func TestParseListModelsResponseFallsBackToAlternateFieldNames(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"models":[
		{"id":"claude-sonnet-4.6","displayName":"Claude Sonnet 4.6"},
		{"modelIdentifier":"gpt-5.6-sol","name":"GPT 5.6 Sol"}
	]}`)
	models, err := ParseListModelsResponse(raw)
	require.NoError(t, err)
	require.Equal(t, []ModelInfo{
		{ID: "claude-sonnet-4.6", Name: "Claude Sonnet 4.6"},
		{ID: "gpt-5.6-sol", Name: "GPT 5.6 Sol"},
	}, models)
}

func TestParseListModelsResponseSkipsEntriesWithoutAnyIDField(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"models":[{"modelName":"nameless entry has no id"},{"modelId":"claude-opus-5"}]}`)
	models, err := ParseListModelsResponse(raw)
	require.NoError(t, err)
	require.Equal(t, []ModelInfo{{ID: "claude-opus-5", Name: ""}}, models)
}

func TestParseListModelsResponseEmptyModelsList(t *testing.T) {
	t.Parallel()

	models, err := ParseListModelsResponse([]byte(`{"models":[]}`))
	require.NoError(t, err)
	require.Empty(t, models)
}

func TestParseListModelsResponseRejectsInvalidJSON(t *testing.T) {
	t.Parallel()

	_, err := ParseListModelsResponse([]byte(`not json`))
	require.Error(t, err)
}
