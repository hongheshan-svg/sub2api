//go:build unit

package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/stretchr/testify/require"
)

func kiroListModelsTestAccount() *Account {
	return &Account{
		ID:       1,
		Platform: PlatformKiro,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"auth_method":  "social",
			"access_token": "test-access-token",
			"profile_arn":  kiroTestValidProfileArn,
			"region":       "us-east-1",
		},
	}
}

func TestListAvailableModelsRejectsAPIKeyAccount(t *testing.T) {
	t.Parallel()

	svc := &KiroGatewayService{}
	account := &Account{
		Platform:    PlatformKiro,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"auth_method": "api_key", "api_key": "k"},
	}
	_, err := svc.ListAvailableModels(t.Context(), account)
	require.Error(t, err)
	require.Contains(t, err.Error(), "API Key")
}

func TestListAvailableModelsRejectsNilAccount(t *testing.T) {
	t.Parallel()

	svc := &KiroGatewayService{}
	_, err := svc.ListAvailableModels(t.Context(), nil)
	require.Error(t, err)
}

// TestListAvailableModelsSurfacesRefreshFailure 覆盖 oauthRefreshAPI 未配置
// 时的失败路径——refreshAccountToken 本身要求非 nil，ListAvailableModels
// 必须把这个失败原样透传给调用方，而不是跳过刷新继续用可能过期的 token 硬发。
func TestListAvailableModelsSurfacesRefreshFailure(t *testing.T) {
	t.Parallel()

	svc := &KiroGatewayService{}
	_, err := svc.ListAvailableModels(t.Context(), kiroListModelsTestAccount())
	require.Error(t, err)
}

// listAvailableModels（不含刷新步骤）是下面各测例的目标，绕开需要完整
// OAuthRefreshAPI 依赖链的刷新步骤，单独覆盖请求构造/响应解析逻辑——
// 与 ListAvailableModels 的刷新编排分开验证。

func TestListAvailableModelsBuildsRequestAndParsesResponse(t *testing.T) {
	t.Parallel()

	var (
		gotMethod         string
		gotTarget         string
		gotAuth           string
		gotAccept         string
		gotBodyProfileArn string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotTarget = r.Header.Get("x-amz-target")
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		var body struct {
			ProfileArn string `json:"profileArn"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotBodyProfileArn = body.ProfileArn

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]string{
				{"modelId": "claude-sonnet-4.6", "modelName": "Claude Sonnet 4.6"},
				{"modelId": "claude-opus-5"},
			},
		})
	}))
	t.Cleanup(srv.Close)

	svc := &KiroGatewayService{
		listModelsURLOverride: func(string) string { return srv.URL },
	}

	models, err := svc.listAvailableModels(t.Context(), kiroListModelsTestAccount())
	require.NoError(t, err)
	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, kiro.ListModelsAmzTarget, gotTarget)
	require.Equal(t, "Bearer test-access-token", gotAuth)
	require.Equal(t, "application/json, text/event-stream", gotAccept)
	require.Equal(t, kiroTestValidProfileArn, gotBodyProfileArn)
	require.Equal(t, []kiro.ModelInfo{
		{ID: "claude-sonnet-4.6", Name: "Claude Sonnet 4.6"},
		{ID: "claude-opus-5", Name: ""},
	}, models)
}

func TestListAvailableModelsRequiresProfileArn(t *testing.T) {
	t.Parallel()

	svc := &KiroGatewayService{
		listModelsURLOverride: func(string) string { t.Fatal("should not call upstream without profile_arn"); return "" },
	}
	account := kiroListModelsTestAccount()
	delete(account.Credentials, "profile_arn")

	_, err := svc.listAvailableModels(t.Context(), account)
	require.Error(t, err)
	require.Contains(t, err.Error(), "profile_arn")
}

func TestListAvailableModelsRequiresAccessToken(t *testing.T) {
	t.Parallel()

	svc := &KiroGatewayService{
		listModelsURLOverride: func(string) string { t.Fatal("should not call upstream without access token"); return "" },
	}
	account := kiroListModelsTestAccount()
	delete(account.Credentials, "access_token")

	_, err := svc.listAvailableModels(t.Context(), account)
	require.Error(t, err)
	require.Contains(t, err.Error(), "access token")
}

func TestListAvailableModelsSurfacesNonOKStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"profileArn is not available"}`))
	}))
	t.Cleanup(srv.Close)

	svc := &KiroGatewayService{
		listModelsURLOverride: func(string) string { return srv.URL },
	}

	_, err := svc.listAvailableModels(t.Context(), kiroListModelsTestAccount())
	require.Error(t, err)
	require.Contains(t, err.Error(), "403")
}

func TestListAvailableModelsRejectsEmptyModelsList(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{}})
	}))
	t.Cleanup(srv.Close)

	svc := &KiroGatewayService{
		listModelsURLOverride: func(string) string { return srv.URL },
	}

	_, err := svc.listAvailableModels(t.Context(), kiroListModelsTestAccount())
	require.Error(t, err)
}
