//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestKiroRefresherCanRefresh(t *testing.T) {
	r := NewKiroTokenRefresher(nil)

	oauth := &Account{Platform: PlatformKiro, Credentials: map[string]any{
		"auth_method": "social", "refresh_token": "rt",
	}}
	require.True(t, r.CanRefresh(oauth))

	// API Key 账号不刷新 —— 否则后台循环会持续报错。
	apiKey := &Account{Platform: PlatformKiro, Credentials: map[string]any{
		"auth_method": "api_key", "api_key": "k",
	}}
	require.False(t, r.CanRefresh(apiKey))

	// 无 refresh token。
	require.False(t, r.CanRefresh(&Account{Platform: PlatformKiro, Credentials: map[string]any{
		"auth_method": "social",
	}}))

	// 别的平台。
	require.False(t, r.CanRefresh(&Account{Platform: PlatformAnthropic, Credentials: map[string]any{
		"refresh_token": "rt",
	}}))

	require.False(t, r.CanRefresh(nil))
}

func TestKiroRefresherNeedsRefresh(t *testing.T) {
	r := NewKiroTokenRefresher(nil)
	window := time.Hour

	// 没有 access token → 需要刷新。
	require.True(t, r.NeedsRefresh(&Account{ID: 1, Credentials: map[string]any{
		"refresh_token": "rt",
	}}, window))

	// 没有 expires_at → 需要刷新。
	require.True(t, r.NeedsRefresh(&Account{ID: 1, Credentials: map[string]any{
		"refresh_token": "rt", "access_token": "at",
	}}, window))

	// 远未过期 → 不需要。
	require.False(t, r.NeedsRefresh(&Account{ID: 1, Credentials: map[string]any{
		"refresh_token": "rt", "access_token": "at",
		"expires_at": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	}}, window))

	// 即将过期 → 需要。
	require.True(t, r.NeedsRefresh(&Account{ID: 1, Credentials: map[string]any{
		"refresh_token": "rt", "access_token": "at",
		"expires_at": time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339),
	}}, window))

	// 无 refresh token 时不参与刷新判定。
	require.False(t, r.NeedsRefresh(&Account{ID: 1, Credentials: map[string]any{
		"access_token": "at",
	}}, window))
}

// TestKiroRefresherPreservesExistingCredentials 覆盖关键回归：
// machine_id / fake_thinking / issuer_url 都不在刷新响应里，丢了等于换设备。
func TestKiroRefresherPreservesExistingCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"accessToken":"at_new","refreshToken":"rt_new",
			"expiresIn":3600,"profileArn":"arn:new"}`))
	}))
	defer srv.Close()

	oauthSvc := newTestKiroOAuthService(t, srv)
	r := NewKiroTokenRefresher(oauthSvc)

	account := &Account{ID: 7, Platform: PlatformKiro, Credentials: map[string]any{
		"auth_method":   "social",
		"refresh_token": "rt_old",
		"access_token":  "at_old",
		"machine_id":    "fixed-machine-id",
		"fake_thinking": true,
		"region":        "us-east-1",
	}}

	got, err := r.Refresh(context.Background(), account)
	require.NoError(t, err)

	require.Equal(t, "at_new", got["access_token"])
	require.Equal(t, "rt_new", got["refresh_token"])
	require.Equal(t, "arn:new", got["profile_arn"], "profile_arn 必须回写")

	require.Equal(t, "fixed-machine-id", got["machine_id"], "设备指纹不得因刷新而改变")
	require.Equal(t, true, got["fake_thinking"], "账号级开关不得丢失")
	require.Equal(t, "us-east-1", got["region"])
}

func TestKiroRefresherCacheKey(t *testing.T) {
	r := NewKiroTokenRefresher(nil)
	require.Equal(t, "kiro:account:9", r.CacheKey(&Account{ID: 9}))
}

// TestKiroRefresherImplementsExecutorInterface 保证注册表能接受它。
func TestKiroRefresherImplementsExecutorInterface(t *testing.T) {
	var _ TokenRefresher = (*KiroTokenRefresher)(nil)
	var _ OAuthRefreshExecutor = (*KiroTokenRefresher)(nil)
}
