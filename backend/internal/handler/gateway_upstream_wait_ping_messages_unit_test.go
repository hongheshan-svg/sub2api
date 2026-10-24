//go:build unit

package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const upstreamWaitPingTestSSE = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"stop_reason":null,"usage":{"input_tokens":3,"output_tokens":0}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

// upstreamWaitPingResponse 描述桩上游对某个账号的响应；delay 只作用于该账号的第一次调用，
// 同账号重试时立即返回，避免测试被重试退避拖长。
type upstreamWaitPingResponse struct {
	delay  time.Duration
	status int
	body   string
	// brokenBody 为真时响应体一读就报 unexpected EOF，模拟首个事件前连接被掐断。
	brokenBody bool
}

type upstreamWaitPingUpstream struct {
	service.HTTPUpstream
	mu        sync.Mutex
	responses map[int64]upstreamWaitPingResponse
	calls     []int64
}

func (u *upstreamWaitPingUpstream) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	return u.DoWithTLS(req, proxyURL, accountID, concurrency, nil)
}

func (u *upstreamWaitPingUpstream) DoWithTLS(req *http.Request, _ string, accountID int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	u.mu.Lock()
	firstCall := !slices.Contains(u.calls, accountID)
	u.calls = append(u.calls, accountID)
	r := u.responses[accountID]
	u.mu.Unlock()
	if !firstCall {
		r.delay = 0
	}

	select {
	case <-time.After(r.delay):
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	body := io.NopCloser(strings.NewReader(r.body))
	if r.brokenBody {
		body = io.NopCloser(errReader{io.ErrUnexpectedEOF})
	}
	header := http.Header{}
	if r.status == http.StatusOK {
		header.Set("Content-Type", "text/event-stream")
	} else {
		header.Set("Content-Type", "application/json")
	}
	return &http.Response{StatusCode: r.status, Header: header, Body: body, Request: req}, nil
}

func (u *upstreamWaitPingUpstream) callAccounts() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.calls...)
}

// upstreamWaitPingSettingRepo 让所有设置取默认值。
type upstreamWaitPingSettingRepo struct{ service.SettingRepository }

func (upstreamWaitPingSettingRepo) GetValue(context.Context, string) (string, error) {
	return "", service.ErrSettingNotFound
}

func (upstreamWaitPingSettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

// upstreamWaitPingAccountRepo 吸收换号时的过载冷却、临时不可调度写入。
type upstreamWaitPingAccountRepo struct{ service.AccountRepository }

func (*upstreamWaitPingAccountRepo) SetOverloaded(context.Context, int64, time.Time) error {
	return nil
}

func (*upstreamWaitPingAccountRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	return nil
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func newUpstreamWaitPingAccount(id, groupID int64, priority int) *service.Account {
	return &service.Account{
		ID:            id,
		Name:          "acc",
		Platform:      service.PlatformAnthropic,
		Type:          service.AccountTypeAPIKey,
		Credentials:   map[string]any{"api_key": "sk-test", "base_url": "https://api.anthropic.com"},
		Status:        service.StatusActive,
		Schedulable:   true,
		Concurrency:   1,
		Priority:      priority,
		AccountGroups: []service.AccountGroup{{AccountID: id, GroupID: groupID}},
	}
}

// serveUpstreamWaitPingMessages 走真实 Messages + GatewayService，上游由桩控制；
// ping 间隔取最小的 1 秒，上游延迟 1.3 秒，保证至少发出一拍。账号 ID 越小越先被选中
// （Priority 取 ID，数值小优先）。
func serveUpstreamWaitPingMessages(t *testing.T, responses map[int64]upstreamWaitPingResponse) (*httptest.ResponseRecorder, *upstreamWaitPingUpstream) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	groupID := int64(9600)
	group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive}
	var accounts []*service.Account
	for id := range responses {
		accounts = append(accounts, newUpstreamWaitPingAccount(id, groupID, int(id)))
	}

	upstream := &upstreamWaitPingUpstream{responses: responses}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.UpstreamWaitPingInterval = 1
	accountRepo := &upstreamWaitPingAccountRepo{}
	gwSvc := service.NewGatewayService(
		accountRepo, &fakeGroupRepo{group: group}, nil, nil, nil, nil, nil, nil, cfg,
		service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: accounts}, nil, nil, nil, nil),
		nil, nil,
		service.NewRateLimitService(accountRepo, nil, cfg, nil, nil),
		nil, nil, upstream, nil, nil, nil, nil, nil,
		service.NewSettingService(upstreamWaitPingSettingRepo{}, cfg),
		nil, nil, nil, nil, nil, nil,
	)
	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCacheSvc.Stop)
	h := &GatewayHandler{
		gatewayService:           gwSvc,
		billingCacheService:      billingCacheSvc,
		concurrencyHelper:        NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0),
		maxAccountSwitches:       3,
		maxAccountSwitchesGemini: 1,
		cfg:                      cfg,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := `{"model":"claude-sonnet-4-5","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req.WithContext(context.WithValue(req.Context(), ctxkey.Group, group))
	apiKey := &service.APIKey{
		ID: 9602, UserID: 9603, GroupID: &groupID, Group: group, Status: service.StatusActive,
		User: &service.User{ID: 9603, Concurrency: 10, Balance: 100},
	}
	c.Set(string(middleware.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: 10})

	h.Messages(c)
	return rec, upstream
}

func requireStartsWithPing(t *testing.T, body string) {
	t.Helper()
	require.True(t, strings.HasPrefix(body, upstreamWaitPingFrame), "上游响应前应先发 ping，got: %q", body)
}

func TestMessages_UpstreamWaitPing_SlowUpstreamStreamsAfterPings(t *testing.T) {
	rec, upstream := serveUpstreamWaitPingMessages(t, map[int64]upstreamWaitPingResponse{
		9611: {delay: 1300 * time.Millisecond, status: http.StatusOK, body: upstreamWaitPingTestSSE},
	})

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []int64{9611}, upstream.callAccounts())
	body := rec.Body.String()
	requireStartsWithPing(t, body)
	require.Contains(t, body, "event: message_start\n")
	require.True(t, strings.HasSuffix(body, "event: message_stop\n"+`data: {"type":"message_stop"}`+"\n\n"), "got: %q", body)
	require.NotContains(t, body, "event: error")
}

// 首个账号在 ping 之后才返回可换号的错误：ping 不算已写出响应，仍换到下一个账号。
func TestMessages_UpstreamWaitPing_FailoverAfterPings(t *testing.T) {
	rec, upstream := serveUpstreamWaitPingMessages(t, map[int64]upstreamWaitPingResponse{
		9621: {delay: 1300 * time.Millisecond, status: 529, body: `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`},
		9622: {status: http.StatusOK, body: upstreamWaitPingTestSSE},
	})

	calls := upstream.callAccounts()
	require.GreaterOrEqual(t, len(calls), 2, "应换号重试")
	require.Equal(t, int64(9621), calls[0])
	require.Equal(t, int64(9622), calls[len(calls)-1])
	body := rec.Body.String()
	requireStartsWithPing(t, body)
	require.Contains(t, body, "event: message_start\n")
	require.NotContains(t, body, "event: error")
}

// ping 之后上游返回不可换号的错误：状态码已是 200，错误转成流内 error 事件，且只发一帧。
func TestMessages_UpstreamWaitPing_UpstreamErrorAfterPingsBecomesErrorEvent(t *testing.T) {
	rec, _ := serveUpstreamWaitPingMessages(t, map[int64]upstreamWaitPingResponse{
		9631: {delay: 1300 * time.Millisecond, status: http.StatusBadRequest, body: `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long"}}`},
	})

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	requireStartsWithPing(t, body)
	require.Equal(t, 1, strings.Count(body, "event: error\n"), "got: %q", body)
	require.Contains(t, body, `"type":"invalid_request_error"`)
	require.Contains(t, body, "prompt is too long")
}

// ping 之后上游在首个事件前断流：只发过 ping 仍可换号。
func TestMessages_UpstreamWaitPing_StreamBrokenBeforeFirstEventFailsOver(t *testing.T) {
	rec, upstream := serveUpstreamWaitPingMessages(t, map[int64]upstreamWaitPingResponse{
		9641: {delay: 1300 * time.Millisecond, status: http.StatusOK, brokenBody: true},
		9642: {status: http.StatusOK, body: upstreamWaitPingTestSSE},
	})

	calls := upstream.callAccounts()
	require.GreaterOrEqual(t, len(calls), 2, "应换号重试，got calls=%v body=%q", calls, rec.Body.String())
	require.Equal(t, int64(9641), calls[0])
	require.Equal(t, int64(9642), calls[len(calls)-1])
	body := rec.Body.String()
	requireStartsWithPing(t, body)
	require.Contains(t, body, "event: message_start\n")
	require.NotContains(t, body, "event: error")
}
