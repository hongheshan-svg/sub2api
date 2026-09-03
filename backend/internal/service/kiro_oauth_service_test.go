//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"
	"unsafe"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/stretchr/testify/require"
)

// kiroSessionStoreLocalOnlyLen 通过反射读取 kiro.SessionStore 未导出的
// localOnly map 长度。TryConsume 只清理 localOnly 会话的 memory 表项，
// 不清理 localOnly 标记本身（见 Task 11 已知的遗留细节）；这个 helper
// 让我们能从 service 包外部真正观察到那条标记是否还挂在那里，
// 而不是只看 Get() 返回值——Get() 在 TryConsume 之后本来就会返回
// false，无法区分「TryConsume 自己够用了」和「Delete 真的被调用了」。
func kiroSessionStoreLocalOnlyLen(t *testing.T, store *kiro.SessionStore) int {
	t.Helper()
	rv := reflect.ValueOf(store).Elem().FieldByName("localOnly")
	rv = reflect.NewAt(rv.Type(), unsafe.Pointer(rv.UnsafeAddr())).Elem() //nolint:gosec // 测试专用反射读取，读取未导出字段验证内部清理行为
	return rv.Len()
}

// newTestKiroOAuthService 返回一个把两个 base URL 都指向 srv 的服务实例。
func newTestKiroOAuthService(t *testing.T, srv *httptest.Server) *KiroOAuthService {
	t.Helper()
	svc := NewKiroOAuthService(nil)
	t.Cleanup(svc.Stop)
	svc.oidcBase = func(string) string { return srv.URL }
	svc.socialBase = func(string) string { return srv.URL }
	return svc
}

func TestKiroGenerateAuthURLRegistersClientAndStoresSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/client/register", r.URL.Path)
		_, _ = w.Write([]byte(`{"clientId":"cid","clientSecret":"csec"}`))
	}))
	defer srv.Close()

	svc := newTestKiroOAuthService(t, srv)

	res, err := svc.GenerateAuthURL(context.Background(), &KiroAuthURLInput{
		RedirectURI: "https://gw.example.com/admin/kiro/oauth/callback",
		IssuerURL:   "https://d-90667b4f8e.awsapps.com/start",
		Region:      "us-east-1",
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.SessionID)
	require.Positive(t, res.ExpiresIn)

	u, err := url.Parse(res.AuthorizeURL)
	require.NoError(t, err)
	q := u.Query()
	require.Equal(t, "cid", q.Get("client_id"))
	require.Equal(t, "S256", q.Get("code_challenge_method"))
	require.NotEmpty(t, q.Get("state"))

	// 会话必须落库，且带上 PKCE verifier 与客户端凭据。
	sess, ok := svc.sessionStore.Get(context.Background(), res.SessionID)
	require.True(t, ok)
	require.Equal(t, kiro.AuthIdC, sess.Method)
	require.Equal(t, "cid", sess.ClientID)
	require.Equal(t, "csec", sess.ClientSecret)
	require.NotEmpty(t, sess.Verifier)
	require.Equal(t, q.Get("state"), sess.State)
}

func TestKiroGenerateAuthURLRequiresRedirectAndIssuer(t *testing.T) {
	svc := NewKiroOAuthService(nil)
	defer svc.Stop()

	_, err := svc.GenerateAuthURL(context.Background(), &KiroAuthURLInput{IssuerURL: "https://x/start"})
	require.Error(t, err)

	_, err = svc.GenerateAuthURL(context.Background(), &KiroAuthURLInput{RedirectURI: "https://x/cb"})
	require.Error(t, err)
}

// TestKiroExchangeCodeRejectsStateMismatch 是 CSRF 防护回归。
func TestKiroExchangeCodeRejectsStateMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("state 不匹配时不应发起 token 交换")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	svc := newTestKiroOAuthService(t, srv)
	ctx := context.Background()
	svc.sessionStore.Set(ctx, "sid", &kiro.OAuthSession{
		Method: kiro.AuthIdC, State: "correct", ExpiresAt: time.Now().Add(kiro.SessionTTL),
	})

	_, _, err := svc.ExchangeCode(ctx, &KiroExchangeCodeInput{
		SessionID: "sid", Code: "c", State: "forged",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "state")
}

// TestKiroExchangeCodeIsSingleUse 是重放防护回归：
// 回调 URL 会留在浏览器历史里，第二次兑换必须失败。
func TestKiroExchangeCodeIsSingleUse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/token", r.URL.Path)
		_, _ = w.Write([]byte(`{"accessToken":"at","refreshToken":"rt","expiresIn":3600,"profileArn":"arn:x"}`))
	}))
	defer srv.Close()

	svc := newTestKiroOAuthService(t, srv)
	ctx := context.Background()
	svc.sessionStore.Set(ctx, "sid", &kiro.OAuthSession{
		Method: kiro.AuthIdC, State: "st", ClientID: "cid", ClientSecret: "csec",
		Verifier: "ver", RedirectURI: "https://gw/cb", Region: "us-east-1",
		ExpiresAt: time.Now().Add(kiro.SessionTTL),
	})

	ts, sess, err := svc.ExchangeCode(ctx, &KiroExchangeCodeInput{SessionID: "sid", Code: "c", State: "st"})
	require.NoError(t, err)
	require.Equal(t, "at", ts.AccessToken)
	require.Equal(t, "arn:x", ts.ProfileArn)
	require.Equal(t, kiro.AuthIdC, sess.Method)

	_, _, err = svc.ExchangeCode(ctx, &KiroExchangeCodeInput{SessionID: "sid", Code: "c", State: "st"})
	require.Error(t, err, "同一授权码不得兑换两次")
}

func TestKiroExchangeCodeUnknownSession(t *testing.T) {
	svc := NewKiroOAuthService(nil)
	defer svc.Stop()

	_, _, err := svc.ExchangeCode(context.Background(), &KiroExchangeCodeInput{
		SessionID: "nope", Code: "c", State: "s",
	})
	require.Error(t, err)
}

func TestKiroStartAndPollDeviceAuth(t *testing.T) {
	var tokenCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/client/register":
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Contains(t, body["grantTypes"].([]any),
				"urn:ietf:params:oauth:grant-type:device_code")
			_, _ = w.Write([]byte(`{"clientId":"cid","clientSecret":"csec"}`))
		case "/device_authorization":
			_, _ = w.Write([]byte(`{"deviceCode":"dc","userCode":"ABCD-EFGH",
				"verificationUri":"https://view.awsapps.com/start/#/device",
				"verificationUriComplete":"https://view.awsapps.com/start/#/device?user_code=ABCD-EFGH",
				"expiresIn":600,"interval":5}`))
		case "/token":
			tokenCalls++
			if tokenCalls == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
				return
			}
			_, _ = w.Write([]byte(`{"accessToken":"at","refreshToken":"rt","expiresIn":3600}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	svc := newTestKiroOAuthService(t, srv)
	ctx := context.Background()

	res, err := svc.StartDeviceAuth(ctx, nil, "us-east-1")
	require.NoError(t, err)
	require.Equal(t, "ABCD-EFGH", res.UserCode)
	require.Contains(t, res.VerificationURIComplete, "ABCD-EFGH")
	require.Equal(t, 5, res.Interval)

	// 首次轮询：尚未授权。
	_, _, err = svc.PollDeviceAuth(ctx, res.SessionID, nil)
	require.ErrorIs(t, err, kiro.ErrAuthorizationPending)

	// 会话必须保留，供继续轮询。
	_, ok := svc.sessionStore.Get(ctx, res.SessionID)
	require.True(t, ok, "pending 不得销毁会话")

	// 第二次：成功。
	ts, sess, err := svc.PollDeviceAuth(ctx, res.SessionID, nil)
	require.NoError(t, err)
	require.Equal(t, "at", ts.AccessToken)
	require.Equal(t, kiro.AuthBuilderID, sess.Method)
}

func TestKiroRefreshAccountTokenDispatchesByAuthMethod(t *testing.T) {
	var socialHits, oidcHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/refreshToken":
			socialHits++
		case "/token":
			oidcHits++
		}
		_, _ = w.Write([]byte(`{"accessToken":"at","refreshToken":"rt2","expiresIn":3600,"profileArn":"arn:y"}`))
	}))
	defer srv.Close()

	svc := newTestKiroOAuthService(t, srv)
	ctx := context.Background()

	social := &Account{ID: 1, Platform: PlatformKiro, Credentials: map[string]any{
		"auth_method": "social", "refresh_token": "rt",
	}}
	ts, err := svc.RefreshAccountToken(ctx, social)
	require.NoError(t, err)
	require.Equal(t, "arn:y", ts.ProfileArn)
	require.Equal(t, 1, socialHits)

	idc := &Account{ID: 2, Platform: PlatformKiro, Credentials: map[string]any{
		"auth_method": "idc", "refresh_token": "rt",
		"client_id": "cid", "client_secret": "csec",
	}}
	_, err = svc.RefreshAccountToken(ctx, idc)
	require.NoError(t, err)
	require.Equal(t, 1, oidcHits)
}

func TestKiroRefreshAccountTokenRejectsAPIKeyAccounts(t *testing.T) {
	svc := NewKiroOAuthService(nil)
	defer svc.Stop()

	apiKeyAcc := &Account{ID: 3, Platform: PlatformKiro, Credentials: map[string]any{
		"auth_method": "api_key", "api_key": "k",
	}}
	_, err := svc.RefreshAccountToken(context.Background(), apiKeyAcc)
	require.Error(t, err)
	require.Contains(t, err.Error(), "api_key")
}

// TestKiroBuildAccountCredentialsWritesProfileArn 覆盖 §5.5 第 1 点。
func TestKiroBuildAccountCredentialsWritesProfileArn(t *testing.T) {
	svc := NewKiroOAuthService(nil)
	defer svc.Stop()

	creds := svc.BuildAccountCredentials(KiroCredentialInput{
		TokenSet: &kiro.TokenSet{
			AccessToken: "at", RefreshToken: "rt",
			ProfileArn: "arn:aws:codewhisperer:::profile/ABC",
			ExpiresAt:  time.Now().Add(time.Hour),
		},
		Method:       kiro.AuthIdC,
		Region:       "us-east-1",
		IssuerURL:    "https://d-90667b4f8e.awsapps.com/start",
		ClientID:     "cid",
		ClientSecret: "csec",
	})

	require.Equal(t, "at", creds["access_token"])
	require.Equal(t, "rt", creds["refresh_token"])
	require.Equal(t, "arn:aws:codewhisperer:::profile/ABC", creds["profile_arn"])
	require.Equal(t, "idc", creds["auth_method"])
	require.Equal(t, "us-east-1", creds["region"])
	require.Equal(t, "cid", creds["client_id"])
	require.Equal(t, "csec", creds["client_secret"])
	require.NotEmpty(t, creds["expires_at"])
	require.NotEmpty(t, creds["machine_id"], "首次建号即固化设备指纹")
}

func TestKiroBuildAccountCredentialsOmitsEmptyClientCreds(t *testing.T) {
	svc := NewKiroOAuthService(nil)
	defer svc.Stop()

	creds := svc.BuildAccountCredentials(KiroCredentialInput{
		TokenSet: &kiro.TokenSet{AccessToken: "at", RefreshToken: "rt"},
		Method:   kiro.AuthSocial,
		Region:   "us-east-1",
	})

	require.NotContains(t, creds, "client_id", "social 不产生客户端凭据")
	require.NotContains(t, creds, "client_secret")
	require.Equal(t, "social", creds["auth_method"])
}

// TestKiroExchangeCodeConsumedSessionLeavesNoLocalOnlyResidue 是
// localOnly 泄漏回归（非 brief 要求，本任务分派额外要求）。
//
// kiro.SessionStore.Set() 无条件把每个会话写进内存并按需标记 localOnly；
// TryConsume() 消费 localOnly 会话时只删 memory 表项，不清 localOnly
// 标记本身——这是 Task 11 已知且刻意搁置的细节。ExchangeCode 必须在
// TryConsume 成功后调用 Delete 把 memory/localOnly 一并收干净，否则
// Redis 降级期间每完成一次 IdC 授权就会永久泄漏一条 localOnly 记录。
//
// 仅凭 sessionStore.Get() 观察不到这个区别：TryConsume 单独调用后
// Get() 就已经返回 false 了（因为 memory 表项被删了），无法证明
// Delete() 真的被调用过。所以这里通过反射直接读取 localOnly map 的
// 长度——这是这个包里唯一能证明「不是 TryConsume 顺带够用了」的办法。
func TestKiroExchangeCodeConsumedSessionLeavesNoLocalOnlyResidue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"accessToken":"at","refreshToken":"rt","expiresIn":3600,"profileArn":"arn:x"}`))
	}))
	defer srv.Close()

	svc := newTestKiroOAuthService(t, srv)
	ctx := context.Background()
	svc.sessionStore.Set(ctx, "sid", &kiro.OAuthSession{
		Method: kiro.AuthIdC, State: "st", ClientID: "cid", ClientSecret: "csec",
		Verifier: "ver", RedirectURI: "https://gw/cb", Region: "us-east-1",
		ExpiresAt: time.Now().Add(kiro.SessionTTL),
	})
	// 服务默认的内存会话存储没有配置 Redis，所以这个会话必然落 localOnly 路径
	// （与生产环境 Redis 降级时的路径完全一致）。
	require.Equal(t, 1, kiroSessionStoreLocalOnlyLen(t, svc.sessionStore),
		"Set 之后应当有且仅有一条 localOnly 标记")

	_, _, err := svc.ExchangeCode(ctx, &KiroExchangeCodeInput{SessionID: "sid", Code: "c", State: "st"})
	require.NoError(t, err)

	_, ok := svc.sessionStore.Get(ctx, "sid")
	require.False(t, ok, "会话应当在成功兑换后彻底消失")
	require.Zero(t, kiroSessionStoreLocalOnlyLen(t, svc.sessionStore),
		"ExchangeCode 必须调用 Delete 清理 localOnly 标记，否则每完成一次 IdC 授权就永久泄漏一条记录")
}
