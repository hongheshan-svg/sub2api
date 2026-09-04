//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// --- AWS event-stream 帧构造（复刻 internal/pkg/kiro/eventstream_test.go 与
// stream_test.go 里同名的未导出测试 helper——那两个属于 package kiro，本文件
// 在 package service，无法直接引用，只能照抄同一套编码逻辑）。

func kiroTestBuildFrame(headers [][2]string, payload []byte) []byte {
	var hdr []byte
	for _, kv := range headers {
		name := kv[0]
		val := kv[1]
		hdr = append(hdr, byte(len(name)))
		hdr = append(hdr, name...)
		hdr = append(hdr, 7) // string
		var l [2]byte
		binary.BigEndian.PutUint16(l[:], uint16(len(val)))
		hdr = append(hdr, l[:]...)
		hdr = append(hdr, val...)
	}

	total := uint32(12 + len(hdr) + len(payload) + 4)
	buf := make([]byte, 0, total)
	var u32 [4]byte

	binary.BigEndian.PutUint32(u32[:], total)
	buf = append(buf, u32[:]...)
	binary.BigEndian.PutUint32(u32[:], uint32(len(hdr)))
	buf = append(buf, u32[:]...)

	preludeCRC := crc32.ChecksumIEEE(buf[:8])
	binary.BigEndian.PutUint32(u32[:], preludeCRC)
	buf = append(buf, u32[:]...)

	buf = append(buf, hdr...)
	buf = append(buf, payload...)

	msgCRC := crc32.ChecksumIEEE(buf)
	binary.BigEndian.PutUint32(u32[:], msgCRC)
	buf = append(buf, u32[:]...)

	return buf
}

func kiroTestEventFrame(eventType, payload string) []byte {
	return kiroTestBuildFrame([][2]string{
		{":message-type", "event"},
		{":event-type", eventType},
	}, []byte(payload))
}

func kiroTestConcatFrames(frames ...[]byte) []byte {
	var out []byte
	for _, f := range frames {
		out = append(out, f...)
	}
	return out
}

// --- 假上游：用 httptest 起一个真实的本地 HTTP server，callEndpointOverride
// 把 ForwardUpstream 的每次上游调用都路由过来，而不是连 kiro.EndpointsFor
// 给出的真实 AWS/CLI 域名（单测环境连不通，也不该连）。

func kiroTestFakeUpstream(t *testing.T, handler func(callIndex int) (status int, body []byte)) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := int(atomic.AddInt32(&calls, 1)) - 1
		status, body := handler(idx)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func kiroTestOverrideCallingServer(srv *httptest.Server) func(ctx context.Context, account *Account, ep kiro.Endpoint, payload []byte) (*http.Response, error) {
	return func(ctx context.Context, account *Account, ep kiro.Endpoint, payload []byte) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		return http.DefaultClient.Do(req)
	}
}

// kiroBlockRecorder 记录每一次 BlockAccountScheduling 调用，
// 用于红线断言："绝不能因为 INVALID_MODEL_ID 禁用账号"。
type kiroBlockRecorder struct {
	mu     sync.Mutex
	blocks []kiroBlockCall
}

type kiroBlockCall struct {
	accountID int64
	until     time.Time
	reason    string
}

func newKiroBlockRecorder() *kiroBlockRecorder {
	return &kiroBlockRecorder{}
}

func (r *kiroBlockRecorder) BlockAccountScheduling(account *Account, until time.Time, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var accountID int64
	if account != nil {
		accountID = account.ID
	}
	r.blocks = append(r.blocks, kiroBlockCall{accountID: accountID, until: until, reason: reason})
}

func (r *kiroBlockRecorder) ClearAccountSchedulingBlock(int64) {}

func (r *kiroBlockRecorder) called() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.blocks) > 0
}

var _ AccountRuntimeBlocker = (*kiroBlockRecorder)(nil)

func kiroTestOAuthAccount(id int64) *Account {
	return &Account{
		ID:       id,
		Platform: PlatformKiro,
		Credentials: map[string]any{
			"auth_method":  "social",
			"access_token": "at_1",
			"machine_id":   "stable-machine",
		},
	}
}

const kiroTestRequestBody = `{"model":"claude-sonnet-4-5-20250929","max_tokens":100,"messages":[{"role":"user","content":"hi"}],"stream":true}`

func kiroTestContext() (*httptest.ResponseRecorder, *gin.Context) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader([]byte(kiroTestRequestBody)))
	return recorder, c
}

// TestKiroForwardUpstreamSuccessStreamingWithRealCacheReadTokens 覆盖成功路径：
// 客户端收到完整的 message_start -> content_block_delta -> message_stop，
// 且 ForwardResult.Usage.CacheReadInputTokens 等于 meteringEvent 里的真实值。
func TestKiroForwardUpstreamSuccessStreamingWithRealCacheReadTokens(t *testing.T) {
	frames := kiroTestConcatFrames(
		kiroTestEventFrame("assistantResponseEvent", `{"content":"Hello"}`),
		kiroTestEventFrame("metadataEvent", `{"stopReason":"end_turn"}`),
		kiroTestEventFrame("meteringEvent", `{"unit":"credit","usage":1.5,"cacheReadInputTokens":137,"cacheCreationInputTokens":9}`),
	)

	srv, calls := kiroTestFakeUpstream(t, func(int) (int, []byte) {
		return http.StatusOK, frames
	})

	svc := &KiroGatewayService{}
	svc.callEndpointOverride = kiroTestOverrideCallingServer(srv)

	account := kiroTestOAuthAccount(1)
	recorder, c := kiroTestContext()

	result, err := svc.ForwardUpstream(context.Background(), c, account, []byte(kiroTestRequestBody))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.EqualValues(t, 1, atomic.LoadInt32(calls))

	require.Equal(t, 137, result.Usage.CacheReadInputTokens, "cache token 必须是 meteringEvent 的真实值")
	require.Equal(t, 9, result.Usage.CacheCreationInputTokens)
	require.Positive(t, result.Usage.OutputTokens)

	out := recorder.Body.String()
	require.Contains(t, out, "event: message_start")
	require.Contains(t, out, "event: content_block_delta")
	require.Contains(t, out, "text_delta")
	require.Contains(t, out, "Hello")
	require.Contains(t, out, "event: message_stop")
}

// TestKiroForwardUpstreamFirstEndpoint429ThenSecondSucceeds 覆盖端点级重试：
// 第一个端点 429，第二个端点成功，客户端完全无感（看不到第一次失败的任何痕迹）。
func TestKiroForwardUpstreamFirstEndpoint429ThenSecondSucceeds(t *testing.T) {
	successFrames := kiroTestConcatFrames(
		kiroTestEventFrame("assistantResponseEvent", `{"content":"ok"}`),
	)

	srv, calls := kiroTestFakeUpstream(t, func(idx int) (int, []byte) {
		if idx == 0 {
			return http.StatusTooManyRequests, []byte(`{"message":"rate limited, try again later"}`)
		}
		return http.StatusOK, successFrames
	})

	svc := &KiroGatewayService{}
	svc.callEndpointOverride = kiroTestOverrideCallingServer(srv)

	account := kiroTestOAuthAccount(2)
	recorder, c := kiroTestContext()

	result, err := svc.ForwardUpstream(context.Background(), c, account, []byte(kiroTestRequestBody))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.EqualValues(t, 2, atomic.LoadInt32(calls), "第一个端点 429 后必须换第二个端点重试")

	out := recorder.Body.String()
	require.NotContains(t, out, "rate limited", "客户端不应看到第一个端点失败的任何痕迹")
	require.Contains(t, out, "event: message_start")
	require.Contains(t, out, "event: message_stop")
}

// TestKiroForwardUpstreamBadRequestNeverRetriesOrFailsOver 是红线二的集成测试：
// 400 只应该发起一次上游调用，绝不重试也绝不换端点/换账号。
func TestKiroForwardUpstreamBadRequestNeverRetriesOrFailsOver(t *testing.T) {
	srv, calls := kiroTestFakeUpstream(t, func(int) (int, []byte) {
		return http.StatusBadRequest, []byte(`{"message":"malformed schema"}`)
	})

	svc := &KiroGatewayService{}
	svc.callEndpointOverride = kiroTestOverrideCallingServer(srv)

	account := kiroTestOAuthAccount(3)
	_, c := kiroTestContext()

	result, err := svc.ForwardUpstream(context.Background(), c, account, []byte(kiroTestRequestBody))
	require.Error(t, err)
	require.Nil(t, result)

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, NextAccountStop, failoverErr.NextAccountAction, "400 必须显式挡住失败转移")
	require.EqualValues(t, 1, atomic.LoadInt32(calls), "400 不应重试也不应换端点")
}

// TestKiroForwardUpstreamInvalidModelIDExhaustsEndpointsWithoutBlockingAccount
// 是红线一的集成测试：INVALID_MODEL_ID 在三个端点全部复现后返回错误，
// 但账号绝不能被标记为故障——这是 decideKiroAction 两条红线里最容易被
// 破坏的一条，必须做到断言严丝合缝。
func TestKiroForwardUpstreamInvalidModelIDExhaustsEndpointsWithoutBlockingAccount(t *testing.T) {
	srv, calls := kiroTestFakeUpstream(t, func(int) (int, []byte) {
		return http.StatusBadRequest, []byte(`{"message":"INVALID_MODEL_ID: model not available in this region"}`)
	})

	svc := &KiroGatewayService{}
	svc.callEndpointOverride = kiroTestOverrideCallingServer(srv)

	blocker := newKiroBlockRecorder()
	svc.runtimeBlocker = blocker

	account := kiroTestOAuthAccount(4)
	_, c := kiroTestContext()

	result, err := svc.ForwardUpstream(context.Background(), c, account, []byte(kiroTestRequestBody))
	require.Error(t, err)
	require.Nil(t, result)

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, NextAccountStop, failoverErr.NextAccountAction,
		"红线一：网络/区域问题（INVALID_MODEL_ID）绝不能触发账号转移")

	require.EqualValues(t, 3, atomic.LoadInt32(calls), "OAuth 账号应轮完全部 3 个端点后才中止")

	require.False(t, blocker.called(), "红线一：INVALID_MODEL_ID 绝不能禁用账号")
}
