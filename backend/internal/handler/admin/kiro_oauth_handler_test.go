//go:build unit

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestKiroAuthorizeURLRequiresIssuerAndRedirect(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewKiroOAuthHandler(nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/kiro/oauth/authorize-url",
		strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.AuthorizeURL(c)
	require.GreaterOrEqual(t, w.Code, 400, "缺少必填参数必须报错")
}

// TestKiroCallbackRendersHTMLWithoutLeakingSecrets 覆盖回调页的两条要求：
// 返回给人看的 HTML，且不回显任何敏感值。
func TestKiroCallbackRendersHTMLWithoutLeakingSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewKiroOAuthHandler(nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet,
		"/admin/kiro/oauth/callback?error=access_denied&state=st", nil)

	h.Callback(c)

	require.Contains(t, w.Header().Get("Content-Type"), "text/html")
	body := w.Body.String()
	require.NotContains(t, body, "client_secret")
	require.NotContains(t, body, "code=")
}

// TestKiroCallbackMissingCodeRendersErrorPage 覆盖回调页在没有 error 也没有
// code 的畸形访问下（比如有人直接手改 URL）也走人类可读的错误页，而不是
// panic 或裸 JSON。
func TestKiroCallbackMissingCodeRendersErrorPage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewKiroOAuthHandler(nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/kiro/oauth/callback?session_id=sid", nil)

	h.Callback(c)

	require.Contains(t, w.Header().Get("Content-Type"), "text/html")
	require.GreaterOrEqual(t, w.Code, 400)
}

// TestKiroDevicePollPendingIsNotAnError 是「pending 不是错误」这条契约的
// 唯一守卫：DeviceStart 拿到会话后，AWS 端在真正授权完成前对 /token 轮询
// 恒答 authorization_pending，DevicePoll 必须把它翻译成 200 +
// {"status":"pending","interval":N}，不能让前端轮询被当成失败中止。
//
// KiroOAuthService 的 oidcBase/socialBase 字段未导出，本包（admin）拿不到
// 直接赋值的机会；WithBaseURLs 是专为这类跨包 handler 测试开的口子（见
// kiro_oauth_service.go 上的注释），生产环境的 wire 从不调用它。
func TestKiroDevicePollPendingIsNotAnError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/client/register":
			_, _ = w.Write([]byte(`{"clientId":"cid","clientSecret":"csec"}`))
		case "/device_authorization":
			_, _ = w.Write([]byte(`{"deviceCode":"dc","userCode":"ABCD-EFGH",
				"verificationUri":"https://view.awsapps.com/start/#/device",
				"verificationUriComplete":"https://view.awsapps.com/start/#/device?user_code=ABCD-EFGH",
				"expiresIn":600,"interval":5}`))
		case "/token":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	svc := service.NewKiroOAuthService(nil)
	defer svc.Stop()
	svc = svc.WithBaseURLs(
		func(string) string { return srv.URL },
		func(string) string { return srv.URL },
	)
	h := NewKiroOAuthHandler(svc)

	// DeviceStart 拿一个真实会话，好让 DevicePoll 能查到 client_id/device_code。
	startW := httptest.NewRecorder()
	startC, _ := gin.CreateTestContext(startW)
	startC.Request = httptest.NewRequest(http.MethodPost, "/admin/kiro/oauth/device/start",
		strings.NewReader(`{"region":"us-east-1"}`))
	startC.Request.Header.Set("Content-Type", "application/json")
	h.DeviceStart(startC)
	require.Equal(t, http.StatusOK, startW.Code)

	var startResp struct {
		Data struct {
			SessionID string `json:"session_id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(startW.Body.Bytes(), &startResp))
	require.NotEmpty(t, startResp.Data.SessionID)

	// AWS 端尚未完成授权：/token 恒答 authorization_pending。
	pollBody, err := json.Marshal(map[string]string{"session_id": startResp.Data.SessionID})
	require.NoError(t, err)
	pollW := httptest.NewRecorder()
	pollC, _ := gin.CreateTestContext(pollW)
	pollC.Request = httptest.NewRequest(http.MethodPost, "/admin/kiro/oauth/device/poll", bytes.NewReader(pollBody))
	pollC.Request.Header.Set("Content-Type", "application/json")
	h.DevicePoll(pollC)

	require.Equal(t, http.StatusOK, pollW.Code, "pending 必须是 200，不能是错误状态码")
	var pollResp struct {
		Data struct {
			Status   string `json:"status"`
			Interval int    `json:"interval"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(pollW.Body.Bytes(), &pollResp))
	require.Equal(t, "pending", pollResp.Data.Status)
	require.Positive(t, pollResp.Data.Interval)
}

// TestKiroFetchCredentialsPendingIsNotAnError 覆盖 FetchCredentials 的
// pending 契约：还没轮到 / 已被消费 / 已过期三种情况都无法区分，统一返回
// 200 + {"status":"pending"}，不是 404。
func TestKiroFetchCredentialsPendingIsNotAnError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	svc := service.NewKiroOAuthService(nil)
	defer svc.Stop()
	h := NewKiroOAuthHandler(svc)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/kiro/oauth/credentials/never-stashed", nil)
	c.Params = gin.Params{{Key: "session_id", Value: "never-stashed"}}

	h.FetchCredentials(c)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "pending", resp.Data.Status)
}

// TestKiroFetchCredentialsReturnsStashedCredentialsOnce 覆盖 FetchCredentials
// 的一次性读取：第一次拿到 Callback 暂存的 credentials，第二次必须回落到
// pending（而不是把同一份凭据再吐一次）。
func TestKiroFetchCredentialsReturnsStashedCredentialsOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)

	svc := service.NewKiroOAuthService(nil)
	defer svc.Stop()
	h := NewKiroOAuthHandler(svc)

	svc.StashCredentials(context.Background(), "sid", map[string]any{"access_token": "at"})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/kiro/oauth/credentials/sid", nil)
	c.Params = gin.Params{{Key: "session_id", Value: "sid"}}
	h.FetchCredentials(c)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct {
			Status      string         `json:"status"`
			Credentials map[string]any `json:"credentials"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "ok", resp.Data.Status)
	require.Equal(t, "at", resp.Data.Credentials["access_token"])

	// 第二次：同一个 session_id 必须回落到 pending。
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodGet, "/admin/kiro/oauth/credentials/sid", nil)
	c2.Params = gin.Params{{Key: "session_id", Value: "sid"}}
	h.FetchCredentials(c2)

	var resp2 struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &resp2))
	require.Equal(t, "pending", resp2.Data.Status, "同一 session_id 第二次读取不得再返回凭据")
}
