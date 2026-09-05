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

// --- Fix Round 1：creditsExhaustedCooldownUntil 惊群修复的验证 ---
//
// review 指出的 Important 发现：原实现在每一次触发 SignalCreditsExhausted
// 的失败请求上都独立发起一次现场 getUsageLimits 查询，credits 真耗尽时
// 恰好是一批并发请求同时失败的时刻，会对上游造成惊群。修复分两层：
//  1. 短路层——account 本地副本上已有未过期的限流记录时直接复用；
//  2. singleflight 层——真正同一时刻并发的调用共享同一次现场查询。
//
// creditsExhaustedCooldownUntil 本身不写回 account.Extra（写回是调用方
// finishWithAction 在拿到返回值之后做的事），所以下面两个测试各自独立地
// 覆盖一层：短路测试预置好 account.Extra 再断言零上游调用；并发测试从
// 干净账号出发，只能靠 singleflight 收敛。

// kiroTestCreditsFakeUpstream 起一个假的 getUsageLimits 端点，返回一条已
// 耗尽且带真实 nextDateReset 的用量，并用 atomic 计数每次被命中的次数——
// 与 kiroTestFakeUpstream 同款风格。
func kiroTestCreditsFakeUpstream(t *testing.T, resetAt time.Time, delay time.Duration) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if delay > 0 {
			time.Sleep(delay)
		}
		_, _ = w.Write([]byte(`{
			"subscriptionInfo":{"subscriptionTitle":"KIRO PRO+"},
			"overageConfiguration":{"overageStatus":"ENABLED"},
			"usageBreakdownList":[{
				"resourceType":"AGENTIC_REQUEST",
				"currentUsage":1000,"usageLimit":1000,
				"nextDateReset":` + itoa(resetAt.Unix()) + `
			}]
		}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// TestKiroCreditsExhaustedCooldownShortCircuitsOnExistingRateLimit 覆盖层 1：
// account 本地副本上已经有一条还没过期的 kiroCreditsExhaustedKey 记录（模拟
// "另一个并发请求刚做过这次查询并写回结果"），creditsExhaustedCooldownUntil
// 必须直接复用它，一次都不打上游。
func TestKiroCreditsExhaustedCooldownShortCircuitsOnExistingRateLimit(t *testing.T) {
	future := time.Now().Add(2 * time.Hour)
	srv, hits := kiroTestCreditsFakeUpstream(t, future, 0)

	svc := &KiroGatewayService{}
	svc.creditsQuotaFetcherOverride = &KiroQuotaFetcher{qHostFor: func(*Account) string { return srv.URL }}

	account := kiroTestOAuthAccount(200)
	setAccountModelRateLimitSnapshot(account, kiroCreditsExhaustedKey, future, "kiro_credits_exhausted", time.Now())

	until := svc.creditsExhaustedCooldownUntil(context.Background(), account)
	require.WithinDuration(t, future, until, time.Second)
	require.EqualValues(t, 0, atomic.LoadInt32(hits), "本地已有未过期记录时不应发起任何现场查询")
}

// TestKiroCreditsExhaustedCooldownConcurrentCallsCollapseViaSingleflight 覆盖
// 层 2：从一个干净账号（没有预置限流记录，层 1 帮不上忙）出发，N 个 goroutine
// 用一个启动屏障尽量同时调用 creditsExhaustedCooldownUntil，断言真正打到上游
// 的次数远小于 goroutine 数——singleflight 应该把它们收敛到 1 次。
func TestKiroCreditsExhaustedCooldownConcurrentCallsCollapseViaSingleflight(t *testing.T) {
	reset := time.Now().Add(6 * time.Hour)
	// 给假上游一点人为延迟，扩大"多个 goroutine 同时处于 Do() 内部"的窗口，
	// 让并发收敛效果在没有屏障也能大概率复现；配合下面的启动屏障双重保险。
	srv, hits := kiroTestCreditsFakeUpstream(t, reset, 20*time.Millisecond)

	svc := &KiroGatewayService{}
	svc.creditsQuotaFetcherOverride = &KiroQuotaFetcher{qHostFor: func(*Account) string { return srv.URL }}

	account := kiroTestOAuthAccount(201)

	const goroutines = 30
	results := make([]time.Time, goroutines)

	start := make(chan struct{})
	var ready sync.WaitGroup
	var wg sync.WaitGroup
	ready.Add(goroutines)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			ready.Done()
			<-start
			results[idx] = svc.creditsExhaustedCooldownUntil(context.Background(), account)
		}(i)
	}
	ready.Wait() // 等所有 goroutine 都已启动、卡在屏障前，最大化真正同时调用的概率
	close(start)
	wg.Wait()

	got := atomic.LoadInt32(hits)
	t.Logf("goroutines=%d upstream_hits=%d", goroutines, got)

	// 期望正好收敛到 1 次：30 个 goroutine 共享同一个启动屏障，几乎同时进入
	// creditsExhaustedCooldownUntil，account 又没有预置限流记录（层 1 不生效），
	// 应该全部落进 singleflight.Do 的同一个 flightKey，只有第一个真正发起
	// 请求、其余全部拿到共享结果。留 <=2 的容差是为了容住"个别 goroutine
	// 被调度延迟到第一次 Do() 已经返回之后才进入"这种小概率时序窗口——
	// 出现这种情况时 singleflight 无法再合并，会诚实地发起第二次查询，这
	// 不代表去重失败，只是同一时刻并发窗口没有覆盖到全部 goroutine。
	require.LessOrEqual(t, got, int32(2), "singleflight 应该把 %d 个并发调用收敛到至多 1-2 次现场查询", goroutines)
	require.GreaterOrEqual(t, got, int32(1), "至少要真正发起一次查询，不能全部退回 fallback")

	for i, until := range results {
		require.WithinDuration(t, reset, until, 2*time.Second, "goroutine %d 应该拿到真实 nextDateReset，而不是 fallback 冷却", i)
	}
}

// --- I1 + I2：streamToClient 流内 exception 帧处理的回归 ---
//
// kiroTestExceptionFrame 构造一个 AWS event-stream 异常帧，字段命名/取值风格
// 与 internal/pkg/kiro/stream_test.go 里 TestStreamExceptionFrameReturnsError /
// TestStreamFeedReturnsPartialOutputAlongsideException 用的 buildFrame 调用
// 完全一致（那两个测试属于 package kiro，本文件在 package service，无法直接
// 复用，只能照抄同一套 header 组合）。
func kiroTestExceptionFrame(exceptionType, message string) []byte {
	return kiroTestBuildFrame([][2]string{
		{":message-type", "exception"},
		{":exception-type", exceptionType},
	}, []byte(`{"message":"`+message+`"}`))
}

// TestKiroForwardUpstreamPartialContentBeforeInStreamExceptionIsWrittenToClient
// 是 I2 的回归：同一次 Feed 调用里，正文帧先于 exception 帧到达时，正文帧产出
// 的合法事件（message_start/content_block_start/content_block_delta）必须先
// 写给客户端，不能被"先检查 tErr 再决定要不要写"的旧顺序悄悄吞掉。
//
// 场景设计：文本帧让 translator.SawContent() 变为 true，之后紧跟的 exception
// 帧因此走"已出内容只能截断"分支——优雅结束（Finalize 补发收尾事件），
// ForwardUpstream 返回成功而不是错误。这正是 bug 最隐蔽的地方：即便调用方后
// 续把 Finalize 的收尾事件正常写出，如果开头的事件在 tErr!=nil 分支里被跳过，
// 客户端看到的就是一条缺开头、有结尾的畸形 SSE 流。
func TestKiroForwardUpstreamPartialContentBeforeInStreamExceptionIsWrittenToClient(t *testing.T) {
	combined := kiroTestConcatFrames(
		kiroTestEventFrame("assistantResponseEvent", `{"content":"partial output"}`),
		kiroTestExceptionFrame("ThrottlingException", "slow down"),
	)

	srv, calls := kiroTestFakeUpstream(t, func(int) (int, []byte) {
		return http.StatusOK, combined
	})

	svc := &KiroGatewayService{}
	svc.callEndpointOverride = kiroTestOverrideCallingServer(srv)

	account := kiroTestOAuthAccount(300)
	recorder, c := kiroTestContext()

	result, err := svc.ForwardUpstream(context.Background(), c, account, []byte(kiroTestRequestBody))
	require.NoError(t, err, "已经吐出内容后 exception 帧只能优雅截断，不能让 ForwardUpstream 整体报错")
	require.NotNil(t, result)
	require.EqualValues(t, 1, atomic.LoadInt32(calls))

	out := recorder.Body.String()
	require.Contains(t, out, "event: message_start",
		"I2 回归核心断言：正文帧产出的 message_start 必须先于 exception 处理被写出，"+
			"旧实现会因为先检查 tErr 而把它连同 content_block_start/delta 一起吞掉")
	require.Contains(t, out, "event: content_block_start")
	require.Contains(t, out, "text_delta")
	require.Contains(t, out, "partial output")
	require.Contains(t, out, "event: content_block_stop")
	require.Contains(t, out, "event: message_delta")
	require.Contains(t, out, "event: message_stop")
	require.NotContains(t, out, "slow down", "exception 帧本身的内容不应该被当成正文写给客户端")
}

// TestKiroForwardUpstreamInStreamExceptionWithoutPriorContentRoutesThroughDecideKiroAction
// 是 I1 的回归：流内 exception 帧在还没有吐出任何内容时必须经过
// decideKiroAction/finishWithAction 分类，而不是把裸错误直接原样返回给
// ForwardUpstream 的调用方——修复前调用方拿到的只是一个普通 error，永远
// 触发不了失败转移/账号冷却，鉴权失效/限流/额度耗尽这些信号全部失效。
//
// 帧序列：metadataEvent（"valid" 但不产出任何 SSE 事件、也不置位
// SawContent 的帧）后紧跟一个 ThrottlingException 异常帧。ThrottlingException
// 经 kiro.ClassifyUpstreamError 归类为 SignalRateLimited；decideKiroAction
// 对 (SignalRateLimited, sawContent=false, alreadyRefreshed=false,
// hasMoreEndpoints=false) 的判定是 kiroActionFailoverAccount（hasMoreEndpoints
// 在流内阶段恒为 false——已经成功建立连接，没有"换端点"这个选项）。
func TestKiroForwardUpstreamInStreamExceptionWithoutPriorContentRoutesThroughDecideKiroAction(t *testing.T) {
	combined := kiroTestConcatFrames(
		kiroTestEventFrame("metadataEvent", `{"stopReason":"end_turn"}`),
		kiroTestExceptionFrame("ThrottlingException", "slow down"),
	)

	srv, calls := kiroTestFakeUpstream(t, func(int) (int, []byte) {
		return http.StatusOK, combined
	})

	svc := &KiroGatewayService{}
	svc.callEndpointOverride = kiroTestOverrideCallingServer(srv)
	blocker := newKiroBlockRecorder()
	svc.runtimeBlocker = blocker

	account := kiroTestOAuthAccount(301)
	recorder, c := kiroTestContext()

	result, err := svc.ForwardUpstream(context.Background(), c, account, []byte(kiroTestRequestBody))
	require.Error(t, err, "还没吐出任何内容时，流内 exception 帧必须被当作一次可分类的失败处理")
	require.Nil(t, result)
	require.EqualValues(t, 1, atomic.LoadInt32(calls))

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr,
		"I1 回归核心断言：必须是已分类的 *UpstreamFailoverError，不能是裸的 *kiro.UpstreamError 或其它包装错误")
	require.Equal(t, GatewayFailureReason("kiro_rate_limited"), failoverErr.Reason)
	require.Equal(t, NextAccountLegacyRetry, failoverErr.NextAccountAction,
		"SignalRateLimited 走 FailoverAccount，必须允许换账号重试，不能被误判成 Abort")

	require.True(t, blocker.called(), "分类后的 SignalRateLimited 必须触发账号调度冷却")
	require.Equal(t, "kiro_rate_limited", blocker.blocks[0].reason)
	require.Equal(t, account.ID, blocker.blocks[0].accountID)

	require.Empty(t, recorder.Body.String(), "分类失败必须在写出任何 SSE 事件之前发生——metadataEvent 本身不产出事件")
}

// --- TestConnection：管理端"测试连接"功能 ---
//
// 真实账号联调发现的回归：AccountTestService 里从来没有 Kiro 分支，任何
// Kiro 账号点"测试连接"都会落进通用的 testClaudeAccountConnection，报
// "No API key available"（Kiro 账号创建时 Type 统一填的是 apikey，不管实际
// auth_method）——KiroGatewayService.TestConnection 就是补上的真实实现，
// 完整复用 ForwardUpstream，不是另起一套简化探测。

func TestKiroTestConnectionReturnsResponseText(t *testing.T) {
	frames := kiroTestConcatFrames(
		kiroTestEventFrame("assistantResponseEvent", `{"content":"OK"}`),
		kiroTestEventFrame("metadataEvent", `{"stopReason":"end_turn"}`),
	)

	srv, calls := kiroTestFakeUpstream(t, func(int) (int, []byte) {
		return http.StatusOK, frames
	})

	svc := &KiroGatewayService{}
	svc.callEndpointOverride = kiroTestOverrideCallingServer(srv)

	account := kiroTestOAuthAccount(500)

	result, err := svc.TestConnection(context.Background(), account, "")
	require.NoError(t, err)
	require.Equal(t, "OK", result.Text)
	require.EqualValues(t, 1, atomic.LoadInt32(calls))
}

func TestKiroTestConnectionSurfacesUpstreamFailure(t *testing.T) {
	srv, _ := kiroTestFakeUpstream(t, func(int) (int, []byte) {
		return http.StatusBadRequest, []byte(`{"message":"malformed schema"}`)
	})

	svc := &KiroGatewayService{}
	svc.callEndpointOverride = kiroTestOverrideCallingServer(srv)

	account := kiroTestOAuthAccount(501)

	_, err := svc.TestConnection(context.Background(), account, "")
	require.Error(t, err, "上游拒绝时测试连接必须报错，不能悄悄返回空文本假装成功")
}

// TestKiroTestConnectionRejectsUnsupportedModel 是用户真实账号测试报告的
// 症状在管理端"测试连接"入口的直接覆盖（ForwardUpstream 层面的覆盖见
// TestKiroForwardUpstreamRejectsUnsupportedModel）：选择一个白名单确认不
// 支持的模型时，测试连接必须报错，不能显示"完成"，也不该真的去问上游
// （已知不支持，问了是浪费一次上游调用）。
func TestKiroTestConnectionRejectsUnsupportedModel(t *testing.T) {
	srv, calls := kiroTestFakeUpstream(t, func(int) (int, []byte) {
		t.Fatal("不支持的模型必须在到达上游之前就被拒绝")
		return 0, nil
	})

	svc := &KiroGatewayService{}
	svc.callEndpointOverride = kiroTestOverrideCallingServer(srv)

	account := kiroTestOAuthAccount(504)

	_, err := svc.TestConnection(context.Background(), account, "claude-fable-5-2")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not supported")
	require.EqualValues(t, 0, atomic.LoadInt32(calls))
}

// TestKiroForwardUpstreamRejectsUnsupportedModel 是用户真实账号测试报告的
// 两轮回归收敛后的最终行为：
//  1. 第一轮报告"不管选什么模型都显示完成"——根因是 MapModel 把任何未
//     识别的模型名静默换成 claude-sonnet-4.6 再正常转发。
//  2. 收窄成"不在本地白名单里就直接拒绝"之后，第二轮发现白名单本身漏收
//     了 Kiro 实际支持的 claude-opus-5 一整个家族，被错误拒绝——已在
//     kiroModelAliases 里补齐（见 models.go 与 models_test.go）。
//
// 设计参照 AntigravityGatewayService 的既有约定（一份尽量准确的白名单，
// 命中就映射、未命中就干净拒绝，不转发不确定的请求去问上游）：这里断言
// 一个白名单确认不支持的模型名（claude-fable-5-2，与已确认支持的
// claude-fable-5/claude-fable-5-1 区分开）会在到达上游之前就被拒绝，
// 不浪费一次上游调用。
func TestKiroForwardUpstreamRejectsUnsupportedModel(t *testing.T) {
	srv, calls := kiroTestFakeUpstream(t, func(int) (int, []byte) {
		t.Fatal("不支持的模型必须在到达上游之前就被拒绝")
		return 0, nil
	})

	svc := &KiroGatewayService{}
	svc.callEndpointOverride = kiroTestOverrideCallingServer(srv)

	account := kiroTestOAuthAccount(502)
	recorder, c := kiroTestContext()

	body := []byte(`{"model":"claude-fable-5-2","max_tokens":100,"messages":[{"role":"user","content":"hi"}],"stream":true}`)
	result, err := svc.ForwardUpstream(context.Background(), c, account, body)
	require.Error(t, err)
	require.Nil(t, result)
	require.EqualValues(t, 0, atomic.LoadInt32(calls))

	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "permission_error")
	require.Contains(t, recorder.Body.String(), "claude-fable-5-2")
}

// TestKiroForwardUpstreamRejectsEmptyModel 覆盖同一路径的空模型名情况：
// 连请求都构造不出来，同样不需要（也不应该）浪费一次上游调用。
func TestKiroForwardUpstreamRejectsEmptyModel(t *testing.T) {
	srv, calls := kiroTestFakeUpstream(t, func(int) (int, []byte) {
		t.Fatal("空模型名必须在到达上游之前就被拒绝")
		return 0, nil
	})

	svc := &KiroGatewayService{}
	svc.callEndpointOverride = kiroTestOverrideCallingServer(srv)

	account := kiroTestOAuthAccount(503)
	recorder, c := kiroTestContext()

	body := []byte(`{"model":"","max_tokens":100,"messages":[{"role":"user","content":"hi"}],"stream":true}`)
	result, err := svc.ForwardUpstream(context.Background(), c, account, body)
	require.Error(t, err)
	require.Nil(t, result)
	require.EqualValues(t, 0, atomic.LoadInt32(calls))

	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "permission_error")
}
