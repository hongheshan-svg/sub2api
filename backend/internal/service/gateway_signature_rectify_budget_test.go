package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// rectifyBudgetSettingRepo 只提供整流器配置，其余 key 落回默认值。
type rectifyBudgetSettingRepo struct {
	rectifier string
}

func (r *rectifyBudgetSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if key == SettingKeyRectifierSettings {
		return r.rectifier, nil
	}
	return "", ErrSettingNotFound
}

func (r *rectifyBudgetSettingRepo) Get(_ context.Context, _ string) (*Setting, error) {
	return nil, ErrSettingNotFound
}

func (r *rectifyBudgetSettingRepo) Set(_ context.Context, _, _ string) error { return nil }

func (r *rectifyBudgetSettingRepo) GetMultiple(_ context.Context, _ []string) (map[string]string, error) {
	return map[string]string{}, nil
}

func (r *rectifyBudgetSettingRepo) SetMultiple(_ context.Context, _ map[string]string) error {
	return nil
}

func (r *rectifyBudgetSettingRepo) GetAll(_ context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

func (r *rectifyBudgetSettingRepo) Delete(_ context.Context, _ string) error { return nil }

// rectifyBudgetUpstream 首次请求返回 400 thinking 签名错误（可注入往返耗时），
// 其后的请求（即整流重试）返回 200。
type rectifyBudgetUpstream struct {
	mu         sync.Mutex
	calls      int
	bodies     [][]byte
	firstDelay time.Duration
}

func (u *rectifyBudgetUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	return u.DoWithTLS(req, proxyURL, accountID, accountConcurrency, nil)
}

func (u *rectifyBudgetUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	u.mu.Lock()
	u.calls++
	call := u.calls
	u.mu.Unlock()

	if req != nil && req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(b))
		u.mu.Lock()
		u.bodies = append(u.bodies, b)
		u.mu.Unlock()
	}

	if call == 1 {
		if u.firstDelay > 0 {
			time.Sleep(u.firstDelay)
		}
		// 上游（Anthropic 或其兼容中转）对跨签发方的 thinking signature 的真实回包。
		const errBody = `{"type":"error","error":{"type":"invalid_request_error",` +
			"\"message\":\"messages.1.content.0: Invalid `signature` in `thinking` block\"}}"
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader([]byte(errBody))),
		}, nil
	}

	const okBody = `{"id":"msg_ok","type":"message","role":"assistant",` +
		`"content":[{"type":"text","text":"ok"}],"model":"claude-sonnet-4-5-20250929",` +
		`"usage":{"input_tokens":10,"output_tokens":2}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(okBody))),
	}, nil
}

func (u *rectifyBudgetUpstream) callCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

func newRectifyBudgetTestService(upstream HTTPUpstream) *GatewayService {
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			MaxLineSize: defaultMaxLineSize,
		},
	}
	// API Key 账号走 APIKeySignatureEnabled 分支，需显式开启（默认配置里该项为 false）。
	settingRepo := &rectifyBudgetSettingRepo{
		rectifier: `{"enabled":true,"thinking_signature_enabled":true,"apikey_signature_enabled":true}`,
	}
	return &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		settingService:       NewSettingService(settingRepo, cfg),
		rateLimitService:     &RateLimitService{},
		deferredService:      &DeferredService{},
	}
}

func newRectifyBudgetTestContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	return c
}

func newRectifyBudgetAccount() *Account {
	return &Account{
		ID:          401,
		Name:        "anthropic-relay",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "upstream-anthropic-key",
			"base_url": "https://api.anthropic.com",
		},
		Status:      StatusActive,
		Schedulable: true,
	}
}

// 历史 assistant 消息带一个**非空但对当前上游无效**的 signature：
// 这是账号轮换/failover 后的真实形态，预过滤 (FilterThinkingBlocks) 只检查签名非空，
// 会原样放行，因此只能靠 400 之后的整流重试兜底。
func newRectifyBudgetParsedRequest() *ParsedRequest {
	body := []byte(`{"model":"claude-sonnet-4-5-20250929",` +
		`"thinking":{"type":"enabled","budget_tokens":1024},` +
		`"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"hi"}]},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"prev turn",` +
		`"signature":"stale-signature-issued-by-another-upstream"}]},` +
		`{"role":"user","content":[{"type":"text","text":"again"}]}]}`)
	return &ParsedRequest{
		Body:  NewRequestBodyRef(body),
		Model: "claude-sonnet-4-5-20250929",
	}
}

// 慢的首次请求不得吃掉签名整流的预算。
//
// 回归的缺陷：整流预算原本复用 retryStart（在首次请求发出**之前**起算），
// 于是首次请求耗时 ≥ maxRetryElapsed 时，收到 400 的那一刻预算已经耗尽，
// 整流重试被直接跳过，400 原样透传给客户端。长会话 + 慢上游必然命中，
// 快请求则整流成功——这正是该错误"偶发"的由来。
func TestGatewayForward_SignatureRectify_SlowFirstRequestKeepsRectifyBudget(t *testing.T) {
	// 压缩总体退避预算，让首次请求的耗时超过它；整流预算保持宽裕。
	originalRetry := maxRetryElapsed
	maxRetryElapsed = 50 * time.Millisecond
	t.Cleanup(func() { maxRetryElapsed = originalRetry })

	upstream := &rectifyBudgetUpstream{firstDelay: 120 * time.Millisecond}
	svc := newRectifyBudgetTestService(upstream)

	_, err := svc.Forward(context.Background(), newRectifyBudgetTestContext(),
		newRectifyBudgetAccount(), newRectifyBudgetParsedRequest())
	require.NoError(t, err)

	require.Equal(t, 2, upstream.callCount(),
		"慢的首次请求不得跳过签名整流重试：整流预算必须从收到 400 起算")

	// 整流重试必须真的剥离了 thinking，否则重试毫无意义。
	upstream.mu.Lock()
	retryBody := string(upstream.bodies[1])
	upstream.mu.Unlock()
	require.NotContains(t, retryBody, "stale-signature-issued-by-another-upstream",
		"整流重试应剥离携带无效签名的 thinking block")
}

// 整流预算自身耗尽时仍须止损：不再发起额外请求，直接把 400 透传给调用方。
// 这也刻画了缺陷修复前客户端看到的现象——只是那时预算是被首次请求耗时吃掉的。
func TestGatewayForward_SignatureRectify_ExhaustedRectifyBudgetStopsRetry(t *testing.T) {
	originalRectify := rectifyMaxElapsed
	rectifyMaxElapsed = time.Nanosecond
	t.Cleanup(func() { rectifyMaxElapsed = originalRectify })

	upstream := &rectifyBudgetUpstream{}
	svc := newRectifyBudgetTestService(upstream)

	_, err := svc.Forward(context.Background(), newRectifyBudgetTestContext(),
		newRectifyBudgetAccount(), newRectifyBudgetParsedRequest())
	require.Error(t, err, "整流预算耗尽时上游 400 应透传给调用方")
	require.Contains(t, err.Error(), "Invalid `signature` in `thinking` block")

	require.Equal(t, 1, upstream.callCount(),
		"整流预算耗尽时不得再发起额外上游请求")
}
