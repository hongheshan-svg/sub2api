package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// upstreamWaitPingFrame 与 Anthropic 官方 ping 事件同格式。Anthropic SDK 按 event 名
// 分发，没有 event 行的帧会被整帧丢弃，所以必须带 event: ping。
const upstreamWaitPingFrame = "event: ping\ndata: {\"type\": \"ping\"}\n\n"

// upstreamWaitRawErrorMessageLimit 限制非 JSON 错误体转成流内错误消息时的长度。
const upstreamWaitRawErrorMessageLimit = 1024

// upstreamWaitPinger 在 /v1/messages 流式请求等上游响应期间给客户端发 ping。
//
// 网关拿到上游响应头之前不会给客户端写任何字节，而上游首包可能等很久（Anthropic
// response_header_timeout 默认 600 秒，Kiro 单次请求上限 10 分钟）。前面挂着
// Cloudflare 等反向代理时，源站超过代理的读超时（Cloudflare 100~120 秒）没有输出，
// 代理直接回 524，客户端拿到的是代理的错误页，上游的活也白干了。
//
// 只在转发期间（resume 与 pause 之间）发 ping；选号、等槽位、换号退避由各自的逻辑
// 负责。距上次向客户端写出字节满一个 interval 才发第一拍：快速失败（鉴权、参数、限流、
// 换号耗尽）仍走原来的 JSON + 状态码；第一拍之后状态码固化为 200，之后的错误由
// upstreamWaitPingWriter 转成流内 error 事件。
type upstreamWaitPinger struct {
	mu       sync.Mutex
	c        *gin.Context
	inner    gin.ResponseWriter
	writer   *upstreamWaitPingWriter
	interval time.Duration
	reqDone  <-chan struct{}

	// stop 非 nil 表示处于转发期间、允许发 ping；pause 或请求侧接管 writer 时关闭。
	stop chan struct{}
	// lastActivity 是最近一次向客户端写出字节的时间，下一拍在它之后一个 interval。
	lastActivity time.Time
	pinged       bool
	// pingBytes 是 ping 写出的字节数。ping 不算语义响应，Size() 要扣掉它，
	// 否则 handler 会以为 Forward 已经写过响应而放弃换号。
	pingBytes int
	closed    bool
}

// startUpstreamWaitPinger 为流式请求装上 ping writer；非流式或 interval<=0 时返回 nil，
// nil 上的方法都是空操作。
func startUpstreamWaitPinger(c *gin.Context, stream bool, interval time.Duration) *upstreamWaitPinger {
	if !stream || interval <= 0 || c == nil || c.Writer == nil {
		return nil
	}
	p := &upstreamWaitPinger{
		c:            c,
		inner:        c.Writer,
		interval:     interval,
		lastActivity: time.Now(),
	}
	if c.Request != nil {
		p.reqDone = c.Request.Context().Done()
	}
	p.writer = &upstreamWaitPingWriter{ResponseWriter: c.Writer, p: p}
	c.Writer = p.writer
	return p
}

// resume 在一次转发开始前调用：距上次写出字节满 interval 后开始发 ping。
func (p *upstreamWaitPinger) resume() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.stop != nil {
		return
	}
	stop := make(chan struct{})
	p.stop = stop
	go p.run(stop, time.Until(p.lastActivity.Add(p.interval)))
}

// pause 在一次转发结束后调用，停止 ping 并返回本请求是否发过 ping（响应已固化为
// 200 的 SSE 流）。与 ping 协程经同一把锁建立 happens-before，返回后调用方可以
// 独占 ResponseWriter。
func (p *upstreamWaitPinger) pause() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopLocked()
	return p.pinged
}

// close 停止 ping，补发没攒成完整 JSON 的错误体，并还原 c.Writer。
func (p *upstreamWaitPinger) close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.stopLocked()
	p.closed = true
	p.mu.Unlock()
	p.writer.flushPendingError()
	if current, ok := p.c.Writer.(*upstreamWaitPingWriter); ok && current == p.writer {
		p.c.Writer = p.inner
	}
}

func (p *upstreamWaitPinger) stopLocked() {
	if p.stop != nil {
		close(p.stop)
		p.stop = nil
	}
}

func (p *upstreamWaitPinger) run(stop chan struct{}, delay time.Duration) {
	timer := time.NewTimer(max(delay, 0))
	defer timer.Stop()
	for {
		select {
		case <-stop:
			return
		case <-p.reqDone:
			return
		case <-timer.C:
		}
		if !p.ping(stop) {
			return
		}
		timer.Reset(p.interval)
	}
}

// ping 在锁内写一拍；首拍顺带提交 SSE 响应头。返回 false 表示已停止或写失败。
func (p *upstreamWaitPinger) ping(stop chan struct{}) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stop != stop {
		return false
	}
	if !p.inner.Written() {
		header := p.inner.Header()
		header.Set("Content-Type", "text/event-stream")
		header.Set("Cache-Control", "no-cache")
		header.Set("Connection", "keep-alive")
		header.Set("X-Accel-Buffering", "no")
		p.inner.WriteHeader(http.StatusOK)
	}
	n, err := p.inner.WriteString(upstreamWaitPingFrame)
	p.pingBytes += n
	p.pinged = true
	p.lastActivity = time.Now()
	if err != nil {
		p.stopLocked()
		return false
	}
	p.inner.Flush()
	return true
}

// upstreamWaitPingWriter 包装 gin.ResponseWriter：写侧方法先停 ping（锁内建立
// happens-before）再操作底层 writer；读侧方法只加锁不停 ping。ping 协程直接写
// 底层 writer，不经过本包装。
//
// 响应已提交后（ping 或排队 ping 已发出 200）再设置 >=400 的状态码，说明请求侧在
// 写 JSON 错误响应。状态码已经改不了，原样追加 JSON 只会让客户端的 SSE 解析失败，
// 这里把错误体攒齐后转成 Anthropic 流内 error 事件。
type upstreamWaitPingWriter struct {
	gin.ResponseWriter
	p *upstreamWaitPinger

	errStatus int
	errBody   []byte
}

// takeover 停 ping：请求侧要直接操作 ResponseWriter 了。wrote 表示这次会写出字节。
func (w *upstreamWaitPingWriter) takeover(wrote bool) {
	w.p.mu.Lock()
	w.p.stopLocked()
	if wrote {
		w.p.lastActivity = time.Now()
	}
	w.p.mu.Unlock()
}

func (w *upstreamWaitPingWriter) Header() http.Header {
	w.takeover(false)
	return w.ResponseWriter.Header()
}

func (w *upstreamWaitPingWriter) WriteHeader(code int) {
	w.takeover(false)
	if code >= http.StatusBadRequest && w.ResponseWriter.Written() {
		w.errStatus = code
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *upstreamWaitPingWriter) WriteHeaderNow() {
	w.takeover(false)
	w.ResponseWriter.WriteHeaderNow()
}

func (w *upstreamWaitPingWriter) Write(data []byte) (int, error) {
	w.takeover(true)
	if w.errStatus != 0 {
		return w.bufferError(data), nil
	}
	return w.ResponseWriter.Write(data)
}

func (w *upstreamWaitPingWriter) WriteString(s string) (int, error) {
	w.takeover(true)
	if w.errStatus != 0 {
		return w.bufferError([]byte(s)), nil
	}
	return w.ResponseWriter.WriteString(s)
}

func (w *upstreamWaitPingWriter) Flush() {
	w.takeover(true)
	w.ResponseWriter.Flush()
}

// Size 返回扣除 ping 字节后的已写字节数；只写过 ping 时归一化为 -1（gin 的"未写出"
// 哨兵值），与 Forward 前的快照可比。
func (w *upstreamWaitPingWriter) Size() int {
	w.p.mu.Lock()
	defer w.p.mu.Unlock()
	size := w.ResponseWriter.Size()
	if size < 0 || w.p.pingBytes == 0 {
		return size
	}
	if real := size - w.p.pingBytes; real > 0 {
		return real
	}
	return -1
}

func (w *upstreamWaitPingWriter) Written() bool {
	w.p.mu.Lock()
	defer w.p.mu.Unlock()
	return w.ResponseWriter.Written()
}

func (w *upstreamWaitPingWriter) Status() int {
	w.p.mu.Lock()
	defer w.p.mu.Unlock()
	return w.ResponseWriter.Status()
}

// bufferError 攒错误体；c.JSON / c.Data 一次写完整个 JSON，攒成合法 JSON 就立即发出。
func (w *upstreamWaitPingWriter) bufferError(data []byte) int {
	w.errBody = append(w.errBody, data...)
	if gjson.ValidBytes(w.errBody) {
		w.writeErrorEvent()
	}
	return len(data)
}

func (w *upstreamWaitPingWriter) flushPendingError() {
	if w.errStatus != 0 && len(w.errBody) > 0 {
		w.writeErrorEvent()
	}
}

func (w *upstreamWaitPingWriter) writeErrorEvent() {
	status, body := w.errStatus, w.errBody
	w.errStatus, w.errBody = 0, nil

	errType, code, message := anthropicStreamErrorFields(status, body)
	payload, err := json.Marshal(anthropicStreamErrorEvent{
		Type:  "error",
		Error: anthropicStreamErrorDetail{Type: errType, Code: code, Message: message},
	})
	if err != nil {
		return
	}
	_, _ = w.ResponseWriter.WriteString("event: error\ndata: " + string(payload) + "\n\n")
	w.ResponseWriter.Flush()
	service.MarkOpsStreamError(w.p.c, errType, message, status)
	// 错误已经告知客户端，ensureForwardErrorResponse 不再补写兜底帧。
	service.MarkResponseCommitted(w.p.c)
}

type anthropicStreamErrorEvent struct {
	Type  string                     `json:"type"`
	Error anthropicStreamErrorDetail `json:"error"`
}

type anthropicStreamErrorDetail struct {
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

// anthropicStreamErrorFields 从错误响应体里取出 Anthropic 流内错误事件的字段；
// 兼容 Anthropic 形态、OpenAI 形态和非 JSON 文本。
func anthropicStreamErrorFields(status int, body []byte) (errType, code, message string) {
	if gjson.ValidBytes(body) {
		errType = strings.TrimSpace(gjson.GetBytes(body, "error.type").String())
		code = strings.TrimSpace(gjson.GetBytes(body, "error.code").String())
		message = strings.TrimSpace(gjson.GetBytes(body, "error.message").String())
		if message == "" {
			if errField := gjson.GetBytes(body, "error"); errField.Type == gjson.String {
				message = strings.TrimSpace(errField.String())
			}
		}
		if message == "" {
			message = strings.TrimSpace(gjson.GetBytes(body, "message").String())
		}
	} else {
		message = strings.TrimSpace(string(body))
		if len(message) > upstreamWaitRawErrorMessageLimit {
			message = strings.ToValidUTF8(message[:upstreamWaitRawErrorMessageLimit], "")
		}
	}
	if errType == "" {
		errType = anthropicErrorTypeForStatus(status)
	}
	if message == "" {
		message = http.StatusText(status)
	}
	if message == "" {
		message = "Upstream request failed"
	}
	return errType, code, message
}

func anthropicErrorTypeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case 529:
		return "overloaded_error"
	}
	if status >= http.StatusInternalServerError {
		return "api_error"
	}
	return "invalid_request_error"
}
