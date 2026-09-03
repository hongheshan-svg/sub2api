package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

// kiroOAuthHTTPTimeout 是授权/刷新请求的总超时。
const kiroOAuthHTTPTimeout = 30 * time.Second

// KiroOAuthService 负责 Kiro 账号的授权与令牌刷新。
//
// 两条初始授权路径：
//   - idc：动态注册 → PKCE → /authorize → 自建回调页 → /token
//   - builder_id：动态注册（device_code）→ /device_authorization → 轮询 /token
//
// social 与 api_key 不经过授权流：前者由管理员粘贴 refreshToken，后者直接粘 API Key。
type KiroOAuthService struct {
	sessionStore    *kiro.SessionStore
	credentialStash *kiro.CredentialStash
	proxyRepo       ProxyRepository

	// base URL 做成字段以便测试注入 httptest.Server。
	oidcBase   func(region string) string
	socialBase func(region string) string
}

// NewKiroOAuthService 创建服务，默认使用进程内存会话存储与凭据暂存。
// 生产环境由 wire 注入 Redis 版本（见 WithSessionStore / WithCredentialStash）。
func NewKiroOAuthService(proxyRepo ProxyRepository) *KiroOAuthService {
	return &KiroOAuthService{
		sessionStore:    kiro.NewSessionStore(),
		credentialStash: kiro.NewCredentialStash(),
		proxyRepo:       proxyRepo,
		oidcBase:        kiro.OIDCBase,
		socialBase:      kiro.SocialBase,
	}
}

// WithSessionStore 替换会话存储。Redis 接线留在 wire providers 里，
// 因为 depguard 禁止本包 import go-redis。
func (s *KiroOAuthService) WithSessionStore(store *kiro.SessionStore) *KiroOAuthService {
	if s != nil && store != nil {
		if s.sessionStore != nil {
			s.sessionStore.Stop()
		}
		s.sessionStore = store
	}
	return s
}

// WithCredentialStash 替换凭据暂存。Redis 接线同样留在 wire providers 里。
func (s *KiroOAuthService) WithCredentialStash(stash *kiro.CredentialStash) *KiroOAuthService {
	if s != nil && stash != nil {
		if s.credentialStash != nil {
			s.credentialStash.Stop()
		}
		s.credentialStash = stash
	}
	return s
}

// WithBaseURLs 覆盖 OIDC / social 基地址解析函数。
//
// oidcBase / socialBase 字段本身未导出（见上），本包内的测试可以直接赋值，
// 但跨包的 handler 测试（internal/handler/admin）拿到的只是 *KiroOAuthService
// 指针，没法碰未导出字段——这个方法就是留给它们的注入口，好让 AuthorizeURL /
// DeviceStart / DevicePoll 的端到端 handler 测试能指向本地 httptest.Server
// 而不是打真实的 AWS 端点。生产环境的 ProvideKiroOAuthService 从不调用它，
// NewKiroOAuthService 设的默认值（kiro.OIDCBase / kiro.SocialBase）在生产
// 环境里始终生效。
func (s *KiroOAuthService) WithBaseURLs(oidcBase, socialBase func(region string) string) *KiroOAuthService {
	if s == nil {
		return s
	}
	if oidcBase != nil {
		s.oidcBase = oidcBase
	}
	if socialBase != nil {
		s.socialBase = socialBase
	}
	return s
}

// Stop 释放会话存储与凭据暂存的后台清理。
func (s *KiroOAuthService) Stop() {
	if s == nil {
		return
	}
	if s.sessionStore != nil {
		s.sessionStore.Stop()
	}
	if s.credentialStash != nil {
		s.credentialStash.Stop()
	}
}

// StashCredentials 把兑换出的账号 credentials 暂存一次性存储，供管理面板
// 轮询取回。IdC 授权码流程用浏览器整页跳转完成回调，前端 JS 拿不到 code，
// 只能靠这个中转把 Callback 兑换出的结果带到前端下一步的建号请求里。
func (s *KiroOAuthService) StashCredentials(ctx context.Context, sessionID string, creds map[string]any) {
	if s == nil || s.credentialStash == nil {
		return
	}
	s.credentialStash.Set(ctx, sessionID, creds)
}

// TakeStashedCredentials 取回并销毁暂存的 credentials（一次性）。
// 找不到时可能是尚未到达、已被消费，或已过期——三者无法区分，调用方
// （FetchCredentials handler）应统一视为 pending，不当错误处理。
func (s *KiroOAuthService) TakeStashedCredentials(ctx context.Context, sessionID string) (map[string]any, bool) {
	if s == nil || s.credentialStash == nil {
		return nil, false
	}
	return s.credentialStash.TakeOnce(ctx, sessionID)
}

// KiroAuthURLInput 是发起 IdC 授权所需的参数。
type KiroAuthURLInput struct {
	ProxyID *int64
	// RedirectURI 必须是本服务可公开访问的回调地址。
	RedirectURI string
	// IssuerURL 是组织的 SSO 门户地址，如 https://d-xxxx.awsapps.com/start。
	IssuerURL string
	Region    string
}

// KiroAuthURLResult 是授权跳转信息。
type KiroAuthURLResult struct {
	SessionID    string `json:"session_id"`
	AuthorizeURL string `json:"authorize_url"`
	ExpiresIn    int    `json:"expires_in"`
}

// GenerateAuthURL 动态注册客户端、生成 PKCE，并返回授权跳转地址。
// 管理员在浏览器打开它，用组织的用户名/密码在 AWS 门户完成登录。
func (s *KiroOAuthService) GenerateAuthURL(ctx context.Context, input *KiroAuthURLInput) (*KiroAuthURLResult, error) {
	if input == nil || strings.TrimSpace(input.RedirectURI) == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "KIRO_OAUTH_REDIRECT_REQUIRED", "redirect URI is required")
	}
	if strings.TrimSpace(input.IssuerURL) == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "KIRO_OAUTH_ISSUER_REQUIRED", "issuer URL is required")
	}

	hc, err := s.httpClient(ctx, input.ProxyID)
	if err != nil {
		return nil, err
	}

	base := s.oidcBase(input.Region)
	reg, err := kiro.RegisterOIDCClient(ctx, hc, base, input.IssuerURL, input.RedirectURI, false)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "KIRO_OAUTH_REGISTER_FAILED", "client registration failed: %v", err)
	}

	pkce, err := kiro.NewPKCE()
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "KIRO_OAUTH_PKCE_FAILED", "failed to generate PKCE: %v", err)
	}
	state, err := kiro.GenerateSessionID()
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "KIRO_OAUTH_STATE_FAILED", "failed to generate state: %v", err)
	}
	sessionID, err := kiro.GenerateSessionID()
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "KIRO_OAUTH_SESSION_FAILED", "failed to generate session id: %v", err)
	}

	s.sessionStore.Set(ctx, sessionID, &kiro.OAuthSession{
		Method:       kiro.AuthIdC,
		ClientID:     reg.ClientID,
		ClientSecret: reg.ClientSecret,
		Verifier:     pkce.Verifier,
		State:        state,
		Region:       input.Region,
		IssuerURL:    input.IssuerURL,
		RedirectURI:  input.RedirectURI,
		ExpiresAt:    time.Now().Add(kiro.SessionTTL),
	})

	return &KiroAuthURLResult{
		SessionID:    sessionID,
		AuthorizeURL: kiro.BuildAuthorizeURL(base, reg.ClientID, input.RedirectURI, state, pkce.Challenge),
		ExpiresIn:    int(kiro.SessionTTL / time.Second),
	}, nil
}

// KiroExchangeCodeInput 是回调兑换所需的参数。
type KiroExchangeCodeInput struct {
	SessionID string
	Code      string
	State     string
	ProxyID   *int64
}

// ExchangeCode 用回调拿到的授权码换取令牌。
//
// 两道安全闸：state 必须匹配（防 CSRF），会话必须能被 TryConsume
// （防重放 —— 回调 URL 会留在浏览器历史里）。
func (s *KiroOAuthService) ExchangeCode(ctx context.Context, input *KiroExchangeCodeInput) (*kiro.TokenSet, *kiro.OAuthSession, error) {
	if input == nil {
		return nil, nil, infraerrors.New(http.StatusBadRequest, "KIRO_OAUTH_INVALID_INPUT", "exchange input is required")
	}

	sess, ok := s.sessionStore.Get(ctx, input.SessionID)
	if !ok {
		return nil, nil, infraerrors.New(http.StatusBadRequest, "KIRO_OAUTH_SESSION_NOT_FOUND", "authorization session not found or expired")
	}

	if subtle.ConstantTimeCompare([]byte(sess.State), []byte(input.State)) != 1 {
		return nil, nil, infraerrors.New(http.StatusBadRequest, "KIRO_OAUTH_STATE_MISMATCH", "authorization state mismatch")
	}
	if strings.TrimSpace(input.Code) == "" {
		return nil, nil, infraerrors.New(http.StatusBadRequest, "KIRO_OAUTH_CODE_REQUIRED", "authorization code is required")
	}

	// 单次消费：失败说明这个回调已经被兑换过。
	if !s.sessionStore.TryConsume(ctx, input.SessionID) {
		return nil, nil, infraerrors.New(http.StatusBadRequest, "KIRO_OAUTH_SESSION_CONSUMED", "authorization session was already used")
	}
	// TryConsume 只在会话落在 localOnly（内存兜底）路径时清理 memory 表项，
	// 不会清理 localOnly 标记本身；这里用 Delete 把 memory/localOnly 一并
	// 收干净，避免 Redis 降级期间每完成一次 IdC 授权就永久泄漏一条
	// localOnly 记录（与 GrokOAuthService.ExchangeCode 的 defer Delete 一致）。
	defer s.sessionStore.Delete(ctx, input.SessionID)

	hc, err := s.httpClient(ctx, input.ProxyID)
	if err != nil {
		return nil, nil, err
	}

	ts, err := kiro.ExchangeAuthorizationCode(ctx, hc, s.oidcBase(sess.Region),
		sess.ClientID, sess.ClientSecret, input.Code, sess.Verifier, sess.RedirectURI)
	if err != nil {
		return nil, nil, infraerrors.Newf(http.StatusBadGateway, "KIRO_OAUTH_EXCHANGE_FAILED", "code exchange failed: %v", err)
	}
	return ts, sess, nil
}

// KiroDeviceAuthResult 是设备码授权的展示信息。
type KiroDeviceAuthResult struct {
	SessionID               string `json:"session_id"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// StartDeviceAuth 发起 Builder ID 的设备码授权。
// 管理员在任意设备打开 VerificationURIComplete 并用账号密码登录批准。
func (s *KiroOAuthService) StartDeviceAuth(ctx context.Context, proxyID *int64, region string) (*KiroDeviceAuthResult, error) {
	hc, err := s.httpClient(ctx, proxyID)
	if err != nil {
		return nil, err
	}

	base := s.oidcBase(region)
	reg, err := kiro.RegisterOIDCClient(ctx, hc, base, kiro.BuilderIDStartURL, "", true)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "KIRO_OAUTH_REGISTER_FAILED", "client registration failed: %v", err)
	}

	da, err := kiro.StartDeviceAuthorization(ctx, hc, base, reg.ClientID, reg.ClientSecret, kiro.BuilderIDStartURL)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "KIRO_OAUTH_DEVICE_START_FAILED", "device authorization failed: %v", err)
	}

	sessionID, err := kiro.GenerateSessionID()
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "KIRO_OAUTH_SESSION_FAILED", "failed to generate session id: %v", err)
	}

	ttl := time.Duration(da.ExpiresIn) * time.Second
	if ttl <= 0 || ttl > kiro.SessionTTL {
		ttl = kiro.SessionTTL
	}

	s.sessionStore.Set(ctx, sessionID, &kiro.OAuthSession{
		Method:       kiro.AuthBuilderID,
		ClientID:     reg.ClientID,
		ClientSecret: reg.ClientSecret,
		Region:       region,
		IssuerURL:    kiro.BuilderIDStartURL,
		DeviceCode:   da.DeviceCode,
		Interval:     da.Interval,
		ExpiresAt:    time.Now().Add(ttl),
	})

	return &KiroDeviceAuthResult{
		SessionID:               sessionID,
		UserCode:                da.UserCode,
		VerificationURI:         da.VerificationURI,
		VerificationURIComplete: da.VerificationURIComplete,
		ExpiresIn:               int(ttl / time.Second),
		Interval:                da.Interval,
	}, nil
}

// PollDeviceAuth 轮询设备码。
//
// 返回 kiro.ErrAuthorizationPending / ErrSlowDown 时**保留会话**供继续轮询；
// 成功或过期后销毁会话。
func (s *KiroOAuthService) PollDeviceAuth(ctx context.Context, sessionID string, proxyID *int64) (*kiro.TokenSet, *kiro.OAuthSession, error) {
	sess, ok := s.sessionStore.Get(ctx, sessionID)
	if !ok {
		return nil, nil, infraerrors.New(http.StatusBadRequest, "KIRO_OAUTH_SESSION_NOT_FOUND", "device authorization session not found or expired")
	}

	hc, err := s.httpClient(ctx, proxyID)
	if err != nil {
		return nil, nil, err
	}

	ts, err := kiro.PollDeviceToken(ctx, hc, s.oidcBase(sess.Region), sess.ClientID, sess.ClientSecret, sess.DeviceCode)
	if err != nil {
		// 非终态：保留会话，让前端按 Interval 继续轮询。
		if errors.Is(err, kiro.ErrAuthorizationPending) || errors.Is(err, kiro.ErrSlowDown) {
			return nil, nil, err
		}
		s.sessionStore.Delete(ctx, sessionID)
		return nil, nil, err
	}

	s.sessionStore.Delete(ctx, sessionID)
	return ts, sess, nil
}

// RefreshAccountToken 按账号的 auth_method 分派到对应刷新端点。
func (s *KiroOAuthService) RefreshAccountToken(ctx context.Context, account *Account) (*kiro.TokenSet, error) {
	if account == nil {
		return nil, errors.New("kiro: account is required")
	}

	method := account.KiroAuthMethod()
	if method == kiro.AuthAPIKey {
		return nil, errors.New("kiro: api_key accounts do not support token refresh")
	}

	refreshToken := account.KiroRefreshToken()
	if refreshToken == "" {
		return nil, errors.New("kiro: account has no refresh token")
	}

	hc, err := s.httpClient(ctx, account.ProxyID)
	if err != nil {
		return nil, err
	}

	region := account.KiroRegion()
	if method.UsesOIDCRefresh() {
		clientID, clientSecret := account.KiroClientCredentials()
		return kiro.RefreshOIDC(ctx, hc, s.oidcBase(region), clientID, clientSecret, refreshToken)
	}
	return kiro.RefreshSocial(ctx, hc, s.socialBase(region), refreshToken)
}

// KiroCredentialInput 是构造账号 credentials 的输入。
type KiroCredentialInput struct {
	TokenSet     *kiro.TokenSet
	Method       kiro.AuthMethod
	Region       string
	IssuerURL    string
	ClientID     string
	ClientSecret string
}

// BuildAccountCredentials 把令牌与授权上下文组装成账号 credentials。
//
// profile_arn 必须写入 —— 漏写会导致账号运行一段时间后 403（设计文档 §5.5 第 1 点）。
// machine_id 在此固化，之后永不变更（§5.5 第 2 点）。
func (s *KiroOAuthService) BuildAccountCredentials(in KiroCredentialInput) map[string]any {
	if in.TokenSet == nil {
		return nil
	}

	creds := map[string]any{
		"auth_method":  string(in.Method),
		"access_token": in.TokenSet.AccessToken,
	}
	if in.TokenSet.RefreshToken != "" {
		creds["refresh_token"] = in.TokenSet.RefreshToken
	}
	if in.TokenSet.ProfileArn != "" {
		creds["profile_arn"] = in.TokenSet.ProfileArn
	}
	if !in.TokenSet.ExpiresAt.IsZero() {
		creds["expires_at"] = in.TokenSet.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if region := strings.TrimSpace(in.Region); region != "" {
		creds["region"] = region
	}
	if issuer := strings.TrimSpace(in.IssuerURL); issuer != "" {
		creds["issuer_url"] = issuer
	}
	if id := strings.TrimSpace(in.ClientID); id != "" {
		creds["client_id"] = id
	}
	if secret := strings.TrimSpace(in.ClientSecret); secret != "" {
		creds["client_secret"] = secret
	}

	// 首次建号即固化设备指纹，之后永不变更。
	EnsureKiroMachineID(creds)

	return creds
}

// httpClient 返回按账号代理配置构建的客户端。
func (s *KiroOAuthService) httpClient(ctx context.Context, proxyID *int64) (*http.Client, error) {
	proxyURL, err := s.proxyURL(ctx, proxyID)
	if err != nil {
		return nil, err
	}
	hc, err := httpclient.GetClient(httpclient.Options{
		ProxyURL: proxyURL,
		Timeout:  kiroOAuthHTTPTimeout,
	})
	if err != nil {
		return nil, infraerrors.Newf(http.StatusServiceUnavailable, "KIRO_OAUTH_CLIENT_FAILED", "failed to build HTTP client: %v", err)
	}
	return hc, nil
}

func (s *KiroOAuthService) proxyURL(ctx context.Context, proxyID *int64) (string, error) {
	if proxyID == nil {
		return "", nil
	}
	if s.proxyRepo == nil {
		return "", infraerrors.New(http.StatusBadRequest, "KIRO_OAUTH_PROXY_NOT_AVAILABLE", "proxy repository is not available")
	}
	proxy, err := s.proxyRepo.GetByID(ctx, *proxyID)
	if err != nil {
		if errors.Is(err, ErrProxyNotFound) {
			return "", infraerrors.New(http.StatusBadRequest, "KIRO_OAUTH_PROXY_NOT_FOUND", "configured proxy was not found")
		}
		return "", infraerrors.New(http.StatusServiceUnavailable, "KIRO_OAUTH_PROXY_LOOKUP_FAILED", "proxy lookup is temporarily unavailable")
	}
	if proxy == nil {
		return "", infraerrors.New(http.StatusBadRequest, "KIRO_OAUTH_PROXY_NOT_FOUND", "configured proxy was not found")
	}
	return proxy.URL(), nil
}
