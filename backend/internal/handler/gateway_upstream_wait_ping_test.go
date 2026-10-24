package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newUpstreamWaitPingTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, context.CancelFunc) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c.Request = httptest.NewRequest(http.MethodPost, EndpointMessages, nil).WithContext(ctx)
	return c, rec, cancel
}

func upstreamWaitPingBytes(p *upstreamWaitPinger) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pingBytes
}

func waitForUpstreamWaitPings(t *testing.T, p *upstreamWaitPinger, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		return upstreamWaitPingBytes(p) >= n*len(upstreamWaitPingFrame)
	}, 2*time.Second, 5*time.Millisecond)
}

func TestUpstreamWaitPinger_DisabledIsNoop(t *testing.T) {
	c, rec, _ := newUpstreamWaitPingTestContext(t)
	original := c.Writer

	require.Nil(t, startUpstreamWaitPinger(c, false, time.Second), "非流式请求不发 ping")
	require.Nil(t, startUpstreamWaitPinger(c, true, 0), "interval=0 表示关闭")
	require.True(t, c.Writer == original)

	var p *upstreamWaitPinger
	p.resume()
	require.False(t, p.pause())
	p.close()
	require.False(t, rec.Flushed)
	require.Zero(t, rec.Body.Len())
}

// 等上游期间按间隔发 Anthropic ping 事件并提交 SSE 响应头；ping 不算语义响应，
// handler 据 Size() 判断是否还能换号。
func TestUpstreamWaitPinger_PingsWhileWaitingAndKeepsFailoverPossible(t *testing.T) {
	c, rec, _ := newUpstreamWaitPingTestContext(t)
	p := startUpstreamWaitPinger(c, true, 10*time.Millisecond)
	defer p.close()

	before := c.Writer.Size()
	p.resume()
	waitForUpstreamWaitPings(t, p, 2)
	require.True(t, p.pause())

	require.Equal(t, before, c.Writer.Size(), "只发过 ping 时 Size 与 Forward 前一致")
	require.True(t, c.Writer.Written())
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	require.Equal(t, "no", rec.Header().Get("X-Accel-Buffering"))
	body := rec.Body.String()
	require.Equal(t, strings.Repeat(upstreamWaitPingFrame, strings.Count(body, "event: ping")), body)

	afterPause := upstreamWaitPingBytes(p)
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, afterPause, upstreamWaitPingBytes(p), "pause 之后不再发 ping")
}

// 首拍之前的快速失败照常返回 JSON + 状态码。
func TestUpstreamWaitPinger_FastErrorKeepsStatusCode(t *testing.T) {
	c, rec, _ := newUpstreamWaitPingTestContext(t)
	p := startUpstreamWaitPinger(c, true, time.Hour)

	p.resume()
	(&GatewayHandler{}).errorResponse(c, http.StatusTooManyRequests, "rate_limit_error", "slow down")
	require.False(t, p.pause())
	p.close()

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	require.Equal(t, "rate_limit_error", gjson.Get(rec.Body.String(), "error.type").String())
	require.Zero(t, upstreamWaitPingBytes(p))
}

// 上游响应到达后，转发逻辑照常设置 SSE 头并写事件，接在 ping 后面。
func TestUpstreamWaitPinger_StreamContinuesAfterPings(t *testing.T) {
	c, rec, _ := newUpstreamWaitPingTestContext(t)
	p := startUpstreamWaitPinger(c, true, 10*time.Millisecond)
	defer p.close()

	p.resume()
	waitForUpstreamWaitPings(t, p, 1)

	event := "event: message_start\ndata: {\"type\":\"message_start\"}\n\n"
	c.Header("Content-Type", "text/event-stream")
	c.Status(http.StatusOK)
	_, err := c.Writer.WriteString(event)
	require.NoError(t, err)
	c.Writer.Flush()
	require.True(t, p.pause())

	require.Equal(t, len(event), c.Writer.Size())
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.True(t, strings.HasSuffix(body, event))
	require.Equal(t, strings.Repeat(upstreamWaitPingFrame, strings.Count(body, "event: ping"))+event, body)
}

// 首拍之后状态码已固化为 200：各种形态的错误响应都转成 Anthropic 流内 error 事件。
func TestUpstreamWaitPinger_ErrorAfterPingBecomesStreamErrorEvent(t *testing.T) {
	tests := []struct {
		name  string
		write func(c *gin.Context)
		want  string
	}{
		{
			name: "handler error JSON",
			write: func(c *gin.Context) {
				(&GatewayHandler{}).errorResponseWithCode(c, http.StatusTooManyRequests, "rate_limit_error", "queue_full", "slow down")
			},
			want: `{"type":"error","error":{"type":"rate_limit_error","code":"queue_full","message":"slow down"}}`,
		},
		{
			name: "OpenAI shaped upstream body",
			write: func(c *gin.Context) {
				c.Data(http.StatusBadGateway, "application/json", []byte(`{"error":{"message":"boom"}}`))
			},
			want: `{"type":"error","error":{"type":"api_error","message":"boom"}}`,
		},
		{
			name: "overloaded status without body fields",
			write: func(c *gin.Context) {
				c.JSON(529, gin.H{})
			},
			want: `{"type":"error","error":{"type":"overloaded_error","message":"Upstream request failed"}}`,
		},
		{
			name: "plain text flushed on close",
			write: func(c *gin.Context) {
				c.String(http.StatusServiceUnavailable, "upstream busy")
			},
			want: `{"type":"error","error":{"type":"api_error","message":"upstream busy"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, rec, _ := newUpstreamWaitPingTestContext(t)
			p := startUpstreamWaitPinger(c, true, 10*time.Millisecond)

			p.resume()
			waitForUpstreamWaitPings(t, p, 1)
			tt.write(c)
			require.True(t, p.pause())
			p.close()

			require.Equal(t, http.StatusOK, rec.Code)
			require.True(t, strings.HasSuffix(rec.Body.String(), "event: error\ndata: "+tt.want+"\n\n"), "got: %q", rec.Body.String())
			require.True(t, service.IsResponseCommitted(c), "错误已告知客户端，handler 不再补写兜底帧")
		})
	}
}

// 错误体里没有消息时用 HTTP 状态文本兜底，非标准状态码（如 529）用通用文案。
func TestAnthropicStreamErrorFields_FallbackMessage(t *testing.T) {
	errType, code, message := anthropicStreamErrorFields(http.StatusBadGateway, []byte(`{}`))
	require.Equal(t, "api_error", errType)
	require.Empty(t, code)
	require.Equal(t, "Bad Gateway", message)

	_, _, message = anthropicStreamErrorFields(http.StatusBadRequest, []byte(`{"error":"bad model"}`))
	require.Equal(t, "bad model", message)

	long := strings.Repeat("x", upstreamWaitRawErrorMessageLimit+10)
	_, _, message = anthropicStreamErrorFields(http.StatusInternalServerError, []byte(long))
	require.Len(t, message, upstreamWaitRawErrorMessageLimit)
}

// 换号退避等间隙不发 ping，但下一次转发开始时若已静默满一个间隔，立即补发。
func TestUpstreamWaitPinger_ResumeAfterSilenceGapPingsImmediately(t *testing.T) {
	c, _, _ := newUpstreamWaitPingTestContext(t)
	p := startUpstreamWaitPinger(c, true, time.Hour)
	defer p.close()

	p.resume()
	time.Sleep(20 * time.Millisecond)
	require.False(t, p.pause())
	require.Zero(t, upstreamWaitPingBytes(p), "静默不满一个间隔不发 ping")

	p.mu.Lock()
	p.lastActivity = time.Now().Add(-time.Hour)
	p.mu.Unlock()
	p.resume()
	waitForUpstreamWaitPings(t, p, 1)
	require.True(t, p.pause())
}

// 请求侧开始操作响应（设置响应头）即接管 writer，本次转发不再发 ping。
func TestUpstreamWaitPinger_WriterTakeoverStopsPings(t *testing.T) {
	c, rec, _ := newUpstreamWaitPingTestContext(t)
	p := startUpstreamWaitPinger(c, true, 10*time.Millisecond)
	defer p.close()

	p.resume()
	c.Header("x-request-id", "req_1")
	time.Sleep(50 * time.Millisecond)
	require.False(t, p.pause())
	require.Zero(t, rec.Body.Len())
}

func TestUpstreamWaitPinger_StopsWhenClientGone(t *testing.T) {
	c, rec, cancel := newUpstreamWaitPingTestContext(t)
	p := startUpstreamWaitPinger(c, true, 10*time.Millisecond)
	defer p.close()

	cancel()
	p.resume()
	time.Sleep(50 * time.Millisecond)
	require.False(t, p.pause())
	require.Zero(t, rec.Body.Len())
}

func TestUpstreamWaitPinger_CloseRestoresWriter(t *testing.T) {
	c, _, _ := newUpstreamWaitPingTestContext(t)
	original := c.Writer
	p := startUpstreamWaitPinger(c, true, time.Second)
	require.False(t, c.Writer == original)

	p.close()
	require.True(t, c.Writer == original)
	p.resume()
	require.False(t, p.pause(), "close 之后 resume 不再启动 ping")
}
