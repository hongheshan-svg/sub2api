# Kiro 平台接入 —— 阶段 1 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 Kiro 成为 sub2api 的一等平台，Claude Code 经 `/v1/messages` 能端到端跑通 Kiro 账号（流式、工具调用、图片），token 自动刷新、额度可见、流量计费落账。

**Architecture:** 新增纯函数层 `internal/pkg/kiro/` 承载 Anthropic ⇄ Kiro 的双向协议转换与 AWS event-stream 解码；service 层照 `antigravity_*` 家族的结构新增 gateway / oauth / token refresher / quota fetcher；调度、粘性会话、失败转移、限流、计费全部复用现有链路，不新建机制。转换层只实现 Anthropic 一对，阶段 3/4 复用现有 `internal/pkg/apicompat` 桥。

**Tech Stack:** Go 1.27.0、Ent、Wire、Gin、testify/require、Vue 3 + pnpm

**Spec:** `docs/superpowers/specs/2026-09-03-kiro-platform-phase1-design.md`

## Global Constraints

- Go 版本锁死 **1.27.0**（`backend/go.mod`）。不要改动。
- 模块路径 **`github.com/Wei-Shaw/sub2api`**（fork 保持上游模块路径）。
- 前端只用 **pnpm**，禁止 npm。改 `package.json` 必须提交 `pnpm-lock.yaml`。
- **depguard**：`internal/service/**` 不得 import `internal/repository`、`gorm.io/gorm`、`github.com/redis/go-redis/v9`；`internal/handler/**` 同。`internal/pkg/**` 不受约束。
- 测试库统一 `github.com/stretchr/testify/require`，pkg 层测试**不加** build tag（对齐 `internal/pkg/xai`），service 层测试加 `//go:build unit`。
- 每个 pkg 层测试函数首行 `t.Parallel()`。
- 提交前必须跑 `cd backend && go test -tags=unit ./...`（**全模块**，窄范围会漏掉其他包的 `//go:build unit` 测试）。
- 改 Ent schema 后 `go generate ./ent`；改 Wire provider set 后 `go generate ./cmd/server`，但 **`wire_gen.go` 的 invoice 块是手工维护的**（`go generate` 会在 invoice 的 `NotificationService` 上失败）—— 用 `go build ./...` 验证，不要盲目接受 regen 结果。
- 本特性 **fork-only**，永不向 upstream 提交。
- 所有新增 Go 文件顶部注释用中文，与仓库现有风格一致。

---

## File Structure

### 新增：`backend/internal/pkg/kiro/`（纯函数层）

| 文件 | 单一职责 |
|---|---|
| `eventstream.go` | AWS event-stream 帧的字节层解码（prelude / headers / payload / CRC） |
| `events.go` | 帧 → 具名事件结构体的解析与分发 |
| `schema.go` | 工具 JSON Schema 的 Kiro 适配清洗 |
| `messages.go` | 消息规整链（合并相邻 / 首条为 user / 强制交替） |
| `request.go` | `apicompat.AnthropicRequest` → Kiro `conversationState` |
| `stream.go` | 事件流 → `[]apicompat.AnthropicStreamEvent` |
| `tokens.go` | input/output token 估算 |
| `models.go` | 模型 ID 映射 |
| `endpoints.go` | 四个上游端点的定义与选择 |
| `errors.go` | 上游错误信号的分类 |

拆分依据：字节层（`eventstream`）与语义层（`events`）分离，便于用 golden 字节做解码器测试而不牵扯业务；请求方向（`messages`+`request`+`schema`）与响应方向（`stream`）分离；`errors` 独立是因为它被 service 层直接消费且是事故高发区。

### 新增：`backend/internal/service/`

`kiro_token_provider.go`、`kiro_token_refresher.go`、`kiro_oauth_service.go`、`kiro_quota_fetcher.go`、`kiro_gateway_service.go`

### 新增：handler / 路由 / 迁移 / 前端

`internal/handler/kiro_gateway_handler.go`、`internal/handler/admin/kiro_oauth_handler.go`、`backend/migrations/234_kiro_platform.sql`、前端账号表单与授权向导。

---

## 任务分组

- **A 组（Task 1-8）** `internal/pkg/kiro` 协议库 —— 纯函数，零外部依赖，可完全独立测试
- **B 组（Task 9-12）** 凭证与授权生命周期
- **C 组（Task 13-16）** 平台常量、网关转发、路由接线
- **D 组（Task 17-18）** 额度与计费
- **E 组（Task 19-20）** 前端

A 组做完即可获得一个完整可测的协议库；B 组之后账号可建可刷新；C 组之后流量跑通；D 组之后额度与计费完备；E 组之后可运维。

---

## A 组：协议库

### Task 1: AWS event-stream 帧解码器

**Files:**
- Create: `backend/internal/pkg/kiro/eventstream.go`
- Test: `backend/internal/pkg/kiro/eventstream_test.go`

**Interfaces:**
- Consumes: 无（本任务是最底层）
- Produces:
  - `type HeaderValue struct { Type uint8; Str string; Bytes []byte; Int int64; Bool bool; UUID [16]byte }`
  - `type Frame struct { Headers map[string]HeaderValue; Payload []byte }`
  - `func (f *Frame) MessageType() string`
  - `func (f *Frame) EventType() string`
  - `func (f *Frame) ExceptionType() string`
  - `func ParseFrame(buf []byte) (*Frame, int, error)` —— 返回 `(nil, 0, nil)` 表示数据不足
  - `type Decoder struct{ ... }`
  - `func NewDecoder() *Decoder`
  - `func (d *Decoder) Feed(chunk []byte) ([]*Frame, error)`
  - `var ErrFrameTooLarge`、`var ErrCRCMismatch`、`var ErrMalformedFrame`

**背景（实现者必读）：** AWS event-stream 的帧格式是

```
[TotalLen u32 BE][HeaderLen u32 BE][PreludeCRC u32 BE][Headers][Payload][MessageCRC u32 BE]
```

- prelude 固定 12 字节（前三个 u32）
- `PreludeCRC` 校验前 8 字节
- `MessageCRC` 校验从帧首到 payload 末尾的全部字节（不含 CRC 自身）
- `TotalLen` 包含自身
- Header 逐条编码：`[nameLen u8][name][valueType u8][value]`
- valueType：`0`=true(无值) `1`=false(无值) `2`=byte(1B) `3`=short(2B) `4`=int(4B) `5`=long(8B) `6`=byteArray(u16 len + data) `7`=string(u16 len + data) `8`=timestamp(8B) `9`=uuid(16B)
- CRC 用 `hash/crc32` 的 IEEE 多项式（`crc32.ChecksumIEEE`）

**参考实现：** 照 `/tmp/kiro-research/kiro2cc-proxy/src/kiro/parser/{frame,header,decoder,crc}.rs` 移植。**不要**照 `kiro-go-proxy/parser/parser.go` —— 它在缓冲区里搜索 `{"content":` 等字面量来切分 JSON，payload 内出现相同字面量时会错切。

- [ ] **Step 1: 写失败测试 —— 单帧解析**

创建 `backend/internal/pkg/kiro/eventstream_test.go`：

```go
package kiro

import (
	"encoding/binary"
	"hash/crc32"
	"testing"

	"github.com/stretchr/testify/require"
)

// buildFrame 按 AWS event-stream 格式组装一个测试帧。
// headers 为有序的 (name, stringValue) 对。
func buildFrame(t *testing.T, headers [][2]string, payload []byte) []byte {
	t.Helper()

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

func TestParseFrameSingle(t *testing.T) {
	t.Parallel()

	raw := buildFrame(t, [][2]string{
		{":message-type", "event"},
		{":event-type", "assistantResponseEvent"},
	}, []byte(`{"content":"hi"}`))

	frame, consumed, err := ParseFrame(raw)
	require.NoError(t, err)
	require.NotNil(t, frame)
	require.Equal(t, len(raw), consumed)
	require.Equal(t, "event", frame.MessageType())
	require.Equal(t, "assistantResponseEvent", frame.EventType())
	require.JSONEq(t, `{"content":"hi"}`, string(frame.Payload))
}

func TestParseFrameIncompleteReturnsNil(t *testing.T) {
	t.Parallel()

	raw := buildFrame(t, [][2]string{{":event-type", "assistantResponseEvent"}}, []byte(`{"content":"x"}`))

	// prelude 都不完整
	frame, consumed, err := ParseFrame(raw[:5])
	require.NoError(t, err)
	require.Nil(t, frame)
	require.Zero(t, consumed)

	// prelude 完整但 body 不完整
	frame, consumed, err = ParseFrame(raw[:len(raw)-3])
	require.NoError(t, err)
	require.Nil(t, frame)
	require.Zero(t, consumed)
}

func TestParseFrameCRCMismatch(t *testing.T) {
	t.Parallel()

	raw := buildFrame(t, [][2]string{{":event-type", "assistantResponseEvent"}}, []byte(`{"content":"x"}`))
	raw[len(raw)-1] ^= 0xFF // 破坏 message CRC

	_, _, err := ParseFrame(raw)
	require.ErrorIs(t, err, ErrCRCMismatch)
}

func TestParseFrameTooLarge(t *testing.T) {
	t.Parallel()

	buf := make([]byte, 12)
	binary.BigEndian.PutUint32(buf[0:4], maxMessageSize+1)
	binary.BigEndian.PutUint32(buf[4:8], 0)
	binary.BigEndian.PutUint32(buf[8:12], crc32.ChecksumIEEE(buf[:8]))

	_, _, err := ParseFrame(buf)
	require.ErrorIs(t, err, ErrFrameTooLarge)
}

func TestDecoderSplitAcrossChunks(t *testing.T) {
	t.Parallel()

	f1 := buildFrame(t, [][2]string{{":event-type", "assistantResponseEvent"}}, []byte(`{"content":"a"}`))
	f2 := buildFrame(t, [][2]string{{":event-type", "assistantResponseEvent"}}, []byte(`{"content":"b"}`))
	stream := append(append([]byte{}, f1...), f2...)

	d := NewDecoder()
	var got []*Frame
	// 逐字节喂入，最严苛的切分
	for i := 0; i < len(stream); i++ {
		frames, err := d.Feed(stream[i : i+1])
		require.NoError(t, err)
		got = append(got, frames...)
	}

	require.Len(t, got, 2)
	require.JSONEq(t, `{"content":"a"}`, string(got[0].Payload))
	require.JSONEq(t, `{"content":"b"}`, string(got[1].Payload))
}

func TestDecoderPayloadContainingFrameLikeLiteral(t *testing.T) {
	t.Parallel()

	// 回归测试：payload 内含 `{"content":` 字面量。
	// 字符串扫描式的解析器（kiro-go-proxy 的做法）会在这里错切。
	payload := []byte(`{"content":"the literal {\"content\": is inside"}`)
	raw := buildFrame(t, [][2]string{{":event-type", "assistantResponseEvent"}}, payload)

	d := NewDecoder()
	frames, err := d.Feed(raw)
	require.NoError(t, err)
	require.Len(t, frames, 1)
	require.Equal(t, payload, frames[0].Payload)
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd backend && go test ./internal/pkg/kiro/ -run TestParseFrame -v
```

Expected: FAIL —— `undefined: ParseFrame` / `undefined: ErrCRCMismatch` 等编译错误。

- [ ] **Step 3: 实现 `eventstream.go`**

```go
// Package kiro 实现 Kiro（Amazon Q Developer / AWS CodeWhisperer 后端）的
// 线协议：AWS event-stream 解码，以及 Anthropic ⇄ Kiro 的双向转换。
//
// 本包是纯函数层：不触碰数据库、缓存或 HTTP 客户端，全部逻辑可单测。
package kiro

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

const (
	// preludeSize 是帧前导的固定长度：TotalLen + HeaderLen + PreludeCRC。
	preludeSize = 12
	// minMessageSize 是一个合法帧的最小长度：前导 + 消息 CRC。
	minMessageSize = preludeSize + 4
	// maxMessageSize 与上游一致，防止畸形 TotalLen 触发巨额分配。
	maxMessageSize = 16 * 1024 * 1024
)

var (
	// ErrFrameTooLarge 表示 TotalLen 超过 maxMessageSize。
	ErrFrameTooLarge = errors.New("kiro: event-stream frame exceeds size limit")
	// ErrCRCMismatch 表示前导或整帧的 CRC32 校验失败。
	ErrCRCMismatch = errors.New("kiro: event-stream CRC mismatch")
	// ErrMalformedFrame 表示帧结构不自洽（长度越界、header 截断等）。
	ErrMalformedFrame = errors.New("kiro: malformed event-stream frame")
)

// Header 值类型标识，取值见 AWS event-stream 规范。
const (
	hdrBoolTrue  uint8 = 0
	hdrBoolFalse uint8 = 1
	hdrByte      uint8 = 2
	hdrShort     uint8 = 3
	hdrInteger   uint8 = 4
	hdrLong      uint8 = 5
	hdrByteArray uint8 = 6
	hdrString    uint8 = 7
	hdrTimestamp uint8 = 8
	hdrUUID      uint8 = 9
)

// HeaderValue 是一条 event-stream header 的值。
// Type 决定哪个字段有效，未使用的字段保持零值。
type HeaderValue struct {
	Type  uint8
	Str   string
	Bytes []byte
	Int   int64
	Bool  bool
	UUID  [16]byte
}

// Frame 是一个解码后的 event-stream 帧。
type Frame struct {
	Headers map[string]HeaderValue
	Payload []byte
}

// MessageType 返回 :message-type header（通常是 "event" 或 "exception"）。
func (f *Frame) MessageType() string { return f.Headers[":message-type"].Str }

// EventType 返回 :event-type header（如 "assistantResponseEvent"）。
func (f *Frame) EventType() string { return f.Headers[":event-type"].Str }

// ExceptionType 返回 :exception-type header，仅异常帧携带。
func (f *Frame) ExceptionType() string { return f.Headers[":exception-type"].Str }

// ParseFrame 尝试从 buf 头部解析出一个完整帧。
//
// 返回值语义：
//   - (frame, n, nil)  成功，消费了 n 字节
//   - (nil, 0, nil)    数据不足，调用方应等待更多字节
//   - (nil, 0, err)    帧损坏，调用方应终止该流
func ParseFrame(buf []byte) (*Frame, int, error) {
	if len(buf) < preludeSize {
		return nil, 0, nil
	}

	total := binary.BigEndian.Uint32(buf[0:4])
	headerLen := binary.BigEndian.Uint32(buf[4:8])
	preludeCRC := binary.BigEndian.Uint32(buf[8:12])

	if crc32.ChecksumIEEE(buf[0:8]) != preludeCRC {
		return nil, 0, fmt.Errorf("%w: prelude", ErrCRCMismatch)
	}
	if total > maxMessageSize {
		return nil, 0, fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, total)
	}
	if total < minMessageSize {
		return nil, 0, fmt.Errorf("%w: total length %d below minimum", ErrMalformedFrame, total)
	}
	if uint64(headerLen)+uint64(minMessageSize) > uint64(total) {
		return nil, 0, fmt.Errorf("%w: header length %d overflows frame", ErrMalformedFrame, headerLen)
	}
	if uint64(len(buf)) < uint64(total) {
		return nil, 0, nil
	}

	msg := buf[:total]
	wantCRC := binary.BigEndian.Uint32(msg[total-4:])
	if crc32.ChecksumIEEE(msg[:total-4]) != wantCRC {
		return nil, 0, fmt.Errorf("%w: message", ErrCRCMismatch)
	}

	headers, err := parseHeaders(msg[preludeSize : preludeSize+headerLen])
	if err != nil {
		return nil, 0, err
	}

	payload := make([]byte, total-4-preludeSize-headerLen)
	copy(payload, msg[preludeSize+headerLen:total-4])

	return &Frame{Headers: headers, Payload: payload}, int(total), nil
}

// parseHeaders 解码 header 区。每条 header 的编码为
// [nameLen u8][name][valueType u8][value]。
func parseHeaders(buf []byte) (map[string]HeaderValue, error) {
	headers := make(map[string]HeaderValue)
	pos := 0

	need := func(n int) error {
		if pos+n > len(buf) {
			return fmt.Errorf("%w: header truncated", ErrMalformedFrame)
		}
		return nil
	}

	for pos < len(buf) {
		if err := need(1); err != nil {
			return nil, err
		}
		nameLen := int(buf[pos])
		pos++

		if err := need(nameLen); err != nil {
			return nil, err
		}
		name := string(buf[pos : pos+nameLen])
		pos += nameLen

		if err := need(1); err != nil {
			return nil, err
		}
		valType := buf[pos]
		pos++

		val := HeaderValue{Type: valType}
		switch valType {
		case hdrBoolTrue:
			val.Bool = true
		case hdrBoolFalse:
			val.Bool = false
		case hdrByte:
			if err := need(1); err != nil {
				return nil, err
			}
			val.Int = int64(int8(buf[pos]))
			pos++
		case hdrShort:
			if err := need(2); err != nil {
				return nil, err
			}
			val.Int = int64(int16(binary.BigEndian.Uint16(buf[pos : pos+2])))
			pos += 2
		case hdrInteger:
			if err := need(4); err != nil {
				return nil, err
			}
			val.Int = int64(int32(binary.BigEndian.Uint32(buf[pos : pos+4])))
			pos += 4
		case hdrLong, hdrTimestamp:
			if err := need(8); err != nil {
				return nil, err
			}
			val.Int = int64(binary.BigEndian.Uint64(buf[pos : pos+8]))
			pos += 8
		case hdrByteArray, hdrString:
			if err := need(2); err != nil {
				return nil, err
			}
			n := int(binary.BigEndian.Uint16(buf[pos : pos+2]))
			pos += 2
			if err := need(n); err != nil {
				return nil, err
			}
			if valType == hdrString {
				val.Str = string(buf[pos : pos+n])
			} else {
				val.Bytes = append([]byte(nil), buf[pos:pos+n]...)
			}
			pos += n
		case hdrUUID:
			if err := need(16); err != nil {
				return nil, err
			}
			copy(val.UUID[:], buf[pos:pos+16])
			pos += 16
		default:
			return nil, fmt.Errorf("%w: unknown header value type %d", ErrMalformedFrame, valType)
		}

		headers[name] = val
	}

	return headers, nil
}

// Decoder 是有状态的流式解码器：把任意切分的字节块累积成完整帧。
// 非并发安全，每个上游响应一个实例。
type Decoder struct {
	buf []byte
}

// NewDecoder 创建一个解码器。
func NewDecoder() *Decoder { return &Decoder{} }

// Feed 追加一段字节并返回其中已完整的帧。
// 返回错误后该 Decoder 不应继续使用。
func (d *Decoder) Feed(chunk []byte) ([]*Frame, error) {
	d.buf = append(d.buf, chunk...)

	var frames []*Frame
	for {
		frame, n, err := ParseFrame(d.buf)
		if err != nil {
			return frames, err
		}
		if frame == nil {
			return frames, nil
		}
		d.buf = d.buf[n:]
		frames = append(frames, frame)
	}
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd backend && go test ./internal/pkg/kiro/ -v
```

Expected: 全部 PASS（6 个测试函数）。

- [ ] **Step 5: 提交**

```bash
cd backend && gofmt -w internal/pkg/kiro/ && go vet ./internal/pkg/kiro/
git add backend/internal/pkg/kiro/eventstream.go backend/internal/pkg/kiro/eventstream_test.go
git commit -m "feat(kiro): AWS event-stream 帧解码器"
```

---

### Task 2: 事件类型与分发

**Files:**
- Create: `backend/internal/pkg/kiro/events.go`
- Test: `backend/internal/pkg/kiro/events_test.go`

**Interfaces:**
- Consumes: Task 1 的 `*Frame`、`Frame.EventType()`、`Frame.MessageType()`、`Frame.ExceptionType()`
- Produces:
  - `type EventKind string` 及常量 `EventAssistantResponse`、`EventToolUse`、`EventMetadata`、`EventMetering`、`EventContextUsage`、`EventCodeReference`、`EventException`、`EventUnknown`
  - `type AssistantResponse struct { Content string; ModelID string }`
  - `type ToolUse struct { Name string; ToolUseID string; Input string; Stop bool }`
  - `type Metadata struct { StopReason string }`
  - `type Metering struct { Unit string; Usage float64; CacheReadInputTokens int; CacheCreationInputTokens int }`
  - `type ContextUsage struct { Percentage float64 }`
  - `type Exception struct { Type string; Code string; Message string }`
  - `type Event struct { Kind EventKind; Assistant *AssistantResponse; ToolUse *ToolUse; Metadata *Metadata; Metering *Metering; ContextUsage *ContextUsage; Exception *Exception }`
  - `func ParseEvent(f *Frame) (Event, error)`

**⚠️ 实现者必读 —— 事件表必须合并两份参考实现：**
`metadataEvent`（承载 `stopReason`）**只有 `Kiro-Go/proxy/kiro.go:677` 处理了**，
`kiro2cc-proxy` 的 `EventType` 枚举里**没有它**。只照 kiro2cc-proxy 抄会静默丢掉
`stop_reason`，导致所有响应的 `stop_reason` 都退化成 `end_turn`，工具调用轮次因此中断。

`stopReason` 在 payload 里的键名两种写法都出现过，需同时尝试 `stopReason` 与 `stop_reason`。

- [ ] **Step 1: 写失败测试**

创建 `backend/internal/pkg/kiro/events_test.go`：

```go
package kiro

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func frameOf(t *testing.T, eventType string, payload string) *Frame {
	t.Helper()
	return &Frame{
		Headers: map[string]HeaderValue{
			":message-type": {Type: hdrString, Str: "event"},
			":event-type":   {Type: hdrString, Str: eventType},
		},
		Payload: []byte(payload),
	}
}

func TestParseEventAssistantResponse(t *testing.T) {
	t.Parallel()

	ev, err := ParseEvent(frameOf(t, "assistantResponseEvent", `{"content":"hello","modelId":"claude-sonnet-4.6"}`))
	require.NoError(t, err)
	require.Equal(t, EventAssistantResponse, ev.Kind)
	require.NotNil(t, ev.Assistant)
	require.Equal(t, "hello", ev.Assistant.Content)
	require.Equal(t, "claude-sonnet-4.6", ev.Assistant.ModelID)
}

func TestParseEventToolUsePartialAndStop(t *testing.T) {
	t.Parallel()

	ev, err := ParseEvent(frameOf(t, "toolUseEvent", `{"name":"Read","toolUseId":"tu_1","input":"{\"pa","stop":false}`))
	require.NoError(t, err)
	require.Equal(t, EventToolUse, ev.Kind)
	require.Equal(t, "Read", ev.ToolUse.Name)
	require.Equal(t, "tu_1", ev.ToolUse.ToolUseID)
	require.Equal(t, `{"pa`, ev.ToolUse.Input)
	require.False(t, ev.ToolUse.Stop)

	ev, err = ParseEvent(frameOf(t, "toolUseEvent", `{"name":"Read","toolUseId":"tu_1","input":"th\":1}","stop":true}`))
	require.NoError(t, err)
	require.True(t, ev.ToolUse.Stop)
}

// TestParseEventMetadataStopReason 是回归测试：metadataEvent 只有 Kiro-Go 处理了，
// kiro2cc-proxy 的事件枚举里没有它。漏掉会导致 stop_reason 永远是 end_turn。
func TestParseEventMetadataStopReason(t *testing.T) {
	t.Parallel()

	ev, err := ParseEvent(frameOf(t, "metadataEvent", `{"stopReason":"max_tokens"}`))
	require.NoError(t, err)
	require.Equal(t, EventMetadata, ev.Kind)
	require.Equal(t, "max_tokens", ev.Metadata.StopReason)

	// 蛇形键名也要认。
	ev, err = ParseEvent(frameOf(t, "metadataEvent", `{"stop_reason":"stop_sequence"}`))
	require.NoError(t, err)
	require.Equal(t, "stop_sequence", ev.Metadata.StopReason)
}

func TestParseEventMetering(t *testing.T) {
	t.Parallel()

	ev, err := ParseEvent(frameOf(t, "meteringEvent",
		`{"unit":"credit","usage":1.5,"cacheReadInputTokens":120,"cacheCreationInputTokens":30}`))
	require.NoError(t, err)
	require.Equal(t, EventMetering, ev.Kind)
	require.InDelta(t, 1.5, ev.Metering.Usage, 1e-9)
	require.Equal(t, 120, ev.Metering.CacheReadInputTokens)
	require.Equal(t, 30, ev.Metering.CacheCreationInputTokens)
}

func TestParseEventContextUsageAndCodeReference(t *testing.T) {
	t.Parallel()

	ev, err := ParseEvent(frameOf(t, "contextUsageEvent", `{"contextUsagePercentage":42.5}`))
	require.NoError(t, err)
	require.Equal(t, EventContextUsage, ev.Kind)
	require.InDelta(t, 42.5, ev.ContextUsage.Percentage, 1e-9)

	ev, err = ParseEvent(frameOf(t, "codeReferenceEvent", `{"references":[]}`))
	require.NoError(t, err)
	require.Equal(t, EventCodeReference, ev.Kind)
}

func TestParseEventException(t *testing.T) {
	t.Parallel()

	f := &Frame{
		Headers: map[string]HeaderValue{
			":message-type":   {Type: hdrString, Str: "exception"},
			":exception-type": {Type: hdrString, Str: "ThrottlingException"},
		},
		Payload: []byte(`{"message":"Too many requests"}`),
	}

	ev, err := ParseEvent(f)
	require.NoError(t, err)
	require.Equal(t, EventException, ev.Kind)
	require.Equal(t, "ThrottlingException", ev.Exception.Type)
	require.Equal(t, "Too many requests", ev.Exception.Message)
}

func TestParseEventUnknownIsNotAnError(t *testing.T) {
	t.Parallel()

	ev, err := ParseEvent(frameOf(t, "someFutureEvent", `{"whatever":1}`))
	require.NoError(t, err)
	require.Equal(t, EventUnknown, ev.Kind)
}

func TestParseEventMalformedPayload(t *testing.T) {
	t.Parallel()

	_, err := ParseEvent(frameOf(t, "assistantResponseEvent", `{not json`))
	require.Error(t, err)
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd backend && go test ./internal/pkg/kiro/ -run TestParseEvent -v
```

Expected: FAIL —— `undefined: ParseEvent`。

- [ ] **Step 3: 实现 `events.go`**

```go
package kiro

import (
	"encoding/json"
	"fmt"
)

// EventKind 是 Kiro event-stream 的事件语义分类。
type EventKind string

const (
	EventAssistantResponse EventKind = "assistantResponseEvent"
	EventToolUse           EventKind = "toolUseEvent"
	EventMetadata          EventKind = "metadataEvent"
	EventMetering          EventKind = "meteringEvent"
	EventContextUsage      EventKind = "contextUsageEvent"
	EventCodeReference     EventKind = "codeReferenceEvent"
	EventException         EventKind = "exception"
	EventUnknown           EventKind = "unknown"
)

// AssistantResponse 是助手输出的一个文本增量。
type AssistantResponse struct {
	Content string `json:"content"`
	ModelID string `json:"modelId"`
}

// ToolUse 是工具调用的一个增量。
// Input 是 JSON 字符串的**分片**，需累积到 Stop 为 true 才是完整参数。
type ToolUse struct {
	Name      string `json:"name"`
	ToolUseID string `json:"toolUseId"`
	Input     string `json:"input"`
	Stop      bool   `json:"stop"`
}

// Metadata 承载上游的终止原因。
//
// 注意：只有 Kiro-Go 处理了这个事件（proxy/kiro.go:677），kiro2cc-proxy 的事件
// 枚举中没有它。漏掉会导致 stop_reason 永远退化为 end_turn。
type Metadata struct {
	StopReason string `json:"stopReason"`
	// StopReasonSnake 兼容蛇形键名的上游变体。
	StopReasonSnake string `json:"stop_reason"`
}

// Metering 是计费事件。Usage 是消耗的 credits；
// 两个 cache 字段是上游给出的**真实** token 数，不需要估算。
type Metering struct {
	Unit                     string  `json:"unit"`
	UnitPlural               string  `json:"unitPlural"`
	Usage                    float64 `json:"usage"`
	CacheReadInputTokens     int     `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int     `json:"cacheCreationInputTokens"`
}

// ContextUsage 是上下文占用比例。
type ContextUsage struct {
	Percentage float64 `json:"contextUsagePercentage"`
}

// Exception 是上游异常帧。
type Exception struct {
	Type    string `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Event 是解析后的具名事件。Kind 决定哪个指针字段非空。
type Event struct {
	Kind         EventKind
	Assistant    *AssistantResponse
	ToolUse      *ToolUse
	Metadata     *Metadata
	Metering     *Metering
	ContextUsage *ContextUsage
	Exception    *Exception
}

// ParseEvent 把一个帧解析为具名事件。
// 未知事件类型返回 EventUnknown 而非错误 —— 上游新增事件不应中断流。
func ParseEvent(f *Frame) (Event, error) {
	if f == nil {
		return Event{Kind: EventUnknown}, nil
	}

	if f.MessageType() == "exception" || f.ExceptionType() != "" {
		ex := &Exception{Type: f.ExceptionType()}
		if len(f.Payload) > 0 {
			if err := json.Unmarshal(f.Payload, ex); err != nil {
				// 异常帧的 payload 不一定是 JSON，退化为原文。
				ex.Message = string(f.Payload)
			}
		}
		return Event{Kind: EventException, Exception: ex}, nil
	}

	kind := EventKind(f.EventType())
	switch kind {
	case EventAssistantResponse:
		var v AssistantResponse
		if err := json.Unmarshal(f.Payload, &v); err != nil {
			return Event{}, fmt.Errorf("kiro: decode assistantResponseEvent: %w", err)
		}
		return Event{Kind: kind, Assistant: &v}, nil

	case EventToolUse:
		var v ToolUse
		if err := json.Unmarshal(f.Payload, &v); err != nil {
			return Event{}, fmt.Errorf("kiro: decode toolUseEvent: %w", err)
		}
		return Event{Kind: kind, ToolUse: &v}, nil

	case EventMetadata:
		var v Metadata
		if err := json.Unmarshal(f.Payload, &v); err != nil {
			return Event{}, fmt.Errorf("kiro: decode metadataEvent: %w", err)
		}
		if v.StopReason == "" {
			v.StopReason = v.StopReasonSnake
		}
		return Event{Kind: kind, Metadata: &v}, nil

	case EventMetering:
		var v Metering
		if err := json.Unmarshal(f.Payload, &v); err != nil {
			return Event{}, fmt.Errorf("kiro: decode meteringEvent: %w", err)
		}
		return Event{Kind: kind, Metering: &v}, nil

	case EventContextUsage:
		var v ContextUsage
		if err := json.Unmarshal(f.Payload, &v); err != nil {
			return Event{}, fmt.Errorf("kiro: decode contextUsageEvent: %w", err)
		}
		return Event{Kind: kind, ContextUsage: &v}, nil

	case EventCodeReference:
		// 开源许可合规追踪，与网关无关，只做识别不解析内容。
		return Event{Kind: kind}, nil

	default:
		return Event{Kind: EventUnknown}, nil
	}
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd backend && go test ./internal/pkg/kiro/ -v
```

Expected: 全部 PASS。

- [ ] **Step 5: 提交**

```bash
cd backend && gofmt -w internal/pkg/kiro/ && go vet ./internal/pkg/kiro/
git add backend/internal/pkg/kiro/events.go backend/internal/pkg/kiro/events_test.go
git commit -m "feat(kiro): event-stream 事件类型与分发（含 metadataEvent）"
```

---

### Task 3: 工具 JSON Schema 清洗

**Files:**
- Create: `backend/internal/pkg/kiro/schema.go`
- Test: `backend/internal/pkg/kiro/schema_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `func SanitizeSchema(schema map[string]any) map[string]any`

**⚠️ 这是历史上 "Kiro400" 事故的根因所在。** Kiro 对工具 schema 的校验比 Anthropic 严格，
不合规的 schema 会让整个请求返回 400，且**换账号重试同样失败**。

清洗规则（按优先级）：
1. 展开 `$ref` / `$defs` / `definitions`（Claude Code 的工具 schema 由 zod/pydantic 生成，普遍带 `$ref`）
2. 删除所有层级的 `additionalProperties`
3. 删除空的 `required` 数组（`"required": []` 会被 Kiro 拒绝）
4. 递归处理 `properties` 的每个子 schema、数组元素、以及嵌套对象

仓库已有 `internal/pkg/antigravity/schema_cleaner.go` 做了 `$ref` 展开 / `allOf` 合并 /
`anyOf` 择优，**实现时先读它**，把 `$ref` 展开部分的思路复用过来，再叠加上面 2-3 两条
Kiro 专属规则。不要 import 它（两个平台的规则会分道扬镳），但要参照其结构。

- [ ] **Step 1: 写失败测试**

创建 `backend/internal/pkg/kiro/schema_test.go`：

```go
package kiro

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func schemaFrom(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &m))
	return m
}

func TestSanitizeSchemaDropsAdditionalProperties(t *testing.T) {
	t.Parallel()

	in := schemaFrom(t, `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"nested": {"type": "object", "additionalProperties": true, "properties": {}}
		}
	}`)

	out := SanitizeSchema(in)

	require.NotContains(t, out, "additionalProperties")
	nested := out["properties"].(map[string]any)["nested"].(map[string]any)
	require.NotContains(t, nested, "additionalProperties")
}

func TestSanitizeSchemaDropsEmptyRequired(t *testing.T) {
	t.Parallel()

	in := schemaFrom(t, `{"type":"object","required":[],"properties":{"a":{"type":"string"}}}`)
	out := SanitizeSchema(in)
	require.NotContains(t, out, "required")

	// 非空 required 必须保留。
	in = schemaFrom(t, `{"type":"object","required":["a"],"properties":{"a":{"type":"string"}}}`)
	out = SanitizeSchema(in)
	require.Equal(t, []any{"a"}, out["required"])
}

// TestSanitizeSchemaFlattensRefs 覆盖 Claude Code 工具 schema 的典型形态：
// zod / pydantic 生成的 schema 普遍带 $ref + $defs。
func TestSanitizeSchemaFlattensRefs(t *testing.T) {
	t.Parallel()

	in := schemaFrom(t, `{
		"type": "object",
		"$defs": {
			"Point": {"type": "object", "properties": {"x": {"type": "number"}}}
		},
		"properties": {
			"origin": {"$ref": "#/$defs/Point"}
		}
	}`)

	out := SanitizeSchema(in)

	require.NotContains(t, out, "$defs")
	origin := out["properties"].(map[string]any)["origin"].(map[string]any)
	require.NotContains(t, origin, "$ref")
	require.Equal(t, "object", origin["type"])
	require.Contains(t, origin["properties"], "x")
}

func TestSanitizeSchemaHandlesArrayItems(t *testing.T) {
	t.Parallel()

	in := schemaFrom(t, `{
		"type": "object",
		"properties": {
			"items": {
				"type": "array",
				"items": {"type": "object", "additionalProperties": false, "required": []}
			}
		}
	}`)

	out := SanitizeSchema(in)
	items := out["properties"].(map[string]any)["items"].(map[string]any)["items"].(map[string]any)
	require.NotContains(t, items, "additionalProperties")
	require.NotContains(t, items, "required")
}

func TestSanitizeSchemaNilAndEmpty(t *testing.T) {
	t.Parallel()

	require.NotNil(t, SanitizeSchema(nil))
	require.Empty(t, SanitizeSchema(map[string]any{}))
}

// TestSanitizeSchemaCyclicRefDoesNotHang 防御自引用 schema 导致的无限递归。
func TestSanitizeSchemaCyclicRefDoesNotHang(t *testing.T) {
	t.Parallel()

	in := schemaFrom(t, `{
		"type": "object",
		"$defs": {"Node": {"type":"object","properties":{"next":{"$ref":"#/$defs/Node"}}}},
		"properties": {"root": {"$ref": "#/$defs/Node"}}
	}`)

	done := make(chan map[string]any, 1)
	go func() { done <- SanitizeSchema(in) }()

	select {
	case out := <-done:
		require.NotNil(t, out)
	case <-timeAfterSeconds(5):
		t.Fatal("SanitizeSchema 在自引用 schema 上没有终止")
	}
}
```

在同一个测试文件底部加入辅助函数（避免 import time 影响可读性）：

```go
func timeAfterSeconds(n int) <-chan time.Time {
	return time.After(time.Duration(n) * time.Second)
}
```

并在 import 块中加入 `"time"`。

- [ ] **Step 2: 运行测试确认失败**

```bash
cd backend && go test ./internal/pkg/kiro/ -run TestSanitizeSchema -v
```

Expected: FAIL —— `undefined: SanitizeSchema`。

- [ ] **Step 3: 阅读现有实现再动手**

```bash
sed -n 1,120p backend/internal/pkg/antigravity/schema_cleaner.go
```

重点看 `extractDefs` / `flattenRefs` / `deepCopy` 三个函数的 `$ref` 展开思路与递归深度控制。

- [ ] **Step 4: 实现 `schema.go`**

```go
package kiro

import "strings"

// maxRefDepth 限制 $ref 展开深度，防止自引用 schema 无限递归。
const maxRefDepth = 16

// SanitizeSchema 把 Anthropic 工具的 input_schema 清洗成 Kiro 可接受的形态。
//
// Kiro 对工具 schema 的校验比 Anthropic 严格，不合规会让**整个请求** 400，
// 且换账号重试同样失败（历史上的 "Kiro400" 事故即源于此）。
//
// 规则：
//  1. 展开 $ref（Claude Code 的工具 schema 由 zod/pydantic 生成，普遍带 $ref/$defs）
//  2. 删除所有层级的 additionalProperties
//  3. 删除空的 required 数组
//
// 入参不会被修改，返回新的 map。
func SanitizeSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return map[string]any{}
	}

	defs := collectDefs(schema)
	cleaned := sanitizeValue(schema, defs, 0)

	out, ok := cleaned.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	delete(out, "$defs")
	delete(out, "definitions")
	return out
}

// collectDefs 收集 $defs 与 definitions 下的可引用子 schema。
func collectDefs(schema map[string]any) map[string]any {
	defs := make(map[string]any)
	for _, key := range []string{"$defs", "definitions"} {
		raw, ok := schema[key].(map[string]any)
		if !ok {
			continue
		}
		for name, val := range raw {
			defs[key+"/"+name] = val
		}
	}
	return defs
}

// resolveRef 按 "#/$defs/Name" 形态查表。找不到返回 nil。
func resolveRef(ref string, defs map[string]any) any {
	trimmed := strings.TrimPrefix(ref, "#/")
	if val, ok := defs[trimmed]; ok {
		return val
	}
	return nil
}

// sanitizeValue 递归清洗任意 JSON 值。
func sanitizeValue(value any, defs map[string]any, depth int) any {
	switch v := value.(type) {
	case map[string]any:
		// $ref 展开：超过深度上限则丢掉 $ref，退化为宽松对象，
		// 保证自引用 schema 能终止。
		if ref, ok := v["$ref"].(string); ok {
			if depth >= maxRefDepth {
				return map[string]any{"type": "object"}
			}
			if target := resolveRef(ref, defs); target != nil {
				merged := sanitizeValue(target, defs, depth+1)
				if mergedMap, ok := merged.(map[string]any); ok {
					// $ref 同级的其他关键字（如 description）保留下来。
					for key, val := range v {
						if key == "$ref" {
							continue
						}
						if _, exists := mergedMap[key]; !exists {
							mergedMap[key] = sanitizeValue(val, defs, depth+1)
						}
					}
					return mergedMap
				}
				return merged
			}
			// 无法解析的 $ref：退化为宽松对象，好过让上游 400。
			return map[string]any{"type": "object"}
		}

		out := make(map[string]any, len(v))
		for key, val := range v {
			switch key {
			case "additionalProperties", "$defs", "definitions", "$schema", "$id":
				continue
			case "required":
				arr, ok := val.([]any)
				if !ok || len(arr) == 0 {
					continue
				}
				out[key] = arr
				continue
			}
			out[key] = sanitizeValue(val, defs, depth+1)
		}
		return out

	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, sanitizeValue(item, defs, depth+1))
		}
		return out

	default:
		return value
	}
}
```

- [ ] **Step 5: 运行测试确认通过**

```bash
cd backend && go test ./internal/pkg/kiro/ -v
```

Expected: 全部 PASS，`TestSanitizeSchemaCyclicRefDoesNotHang` 必须在 5 秒内返回。

- [ ] **Step 6: 提交**

```bash
cd backend && gofmt -w internal/pkg/kiro/ && go vet ./internal/pkg/kiro/
git add backend/internal/pkg/kiro/schema.go backend/internal/pkg/kiro/schema_test.go
git commit -m "feat(kiro): 工具 JSON Schema 清洗（Kiro400 根因修复）"
```

---

### Task 4: 消息规整链

**Files:**
- Create: `backend/internal/pkg/kiro/messages.go`
- Test: `backend/internal/pkg/kiro/messages_test.go`

**Interfaces:**
- Consumes: `github.com/Wei-Shaw/sub2api/internal/pkg/apicompat` 的 `AnthropicRequest`、`AnthropicMessage`、`AnthropicContentBlock`、`AnthropicImageSource`
- Produces:
  - `type Image struct { Format string; Data string }`
  - `type ToolCall struct { ID string; Name string; Input json.RawMessage }`
  - `type ToolResult struct { ToolUseID string; Text string; IsError bool }`
  - `type Msg struct { Role string; Text string; Images []Image; ToolCalls []ToolCall; ToolResults []ToolResult }`
  - `func FlattenSystem(raw json.RawMessage) (string, error)`
  - `func FromAnthropic(req *apicompat.AnthropicRequest) ([]Msg, error)`
  - `func MergeAdjacent(msgs []Msg) []Msg`
  - `func EnsureFirstIsUser(msgs []Msg) []Msg`
  - `func EnsureAlternating(msgs []Msg) []Msg`
  - `func StripToolContent(msgs []Msg) []Msg`
  - `var ErrUnsupportedImageSource`、`var ErrUnsupportedBlock`

**背景（实现者必读）：** Kiro 的 `conversationState.history` 要求**严格 user/assistant 交替
且首条为 user**，而 Anthropic 允许连续同角色消息。转换必须做规整，但规整本身是有损的
（见 spec §6.3）。最容易出 bug 的是：**Anthropic 允许一条 user message 内含多个
`tool_result` block**（Claude Code 并行调用工具时就是这样），这些必须全部聚合进同一条
`Msg.ToolResults`，丢任何一个都会让上游看到不完整的工具轮次而拒绝或胡答。

图片：Kiro 只接受 base64 字节（`{format, source:{bytes}}`）。`AnthropicImageSource.Type`
不是 `"base64"` 时**直接返回错误**，不要静默下载 URL —— 那会给网关开一个 SSRF 面。

- [ ] **Step 1: 写失败测试**

创建 `backend/internal/pkg/kiro/messages_test.go`：

```go
package kiro

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

func rawJSON(t *testing.T, s string) json.RawMessage {
	t.Helper()
	require.True(t, json.Valid([]byte(s)), "测试用例里的 JSON 无效: %s", s)
	return json.RawMessage(s)
}

func TestFlattenSystemStringAndBlocks(t *testing.T) {
	t.Parallel()

	got, err := FlattenSystem(rawJSON(t, `"you are helpful"`))
	require.NoError(t, err)
	require.Equal(t, "you are helpful", got)

	got, err = FlattenSystem(rawJSON(t, `[{"type":"text","text":"a"},{"type":"text","text":"b"}]`))
	require.NoError(t, err)
	require.Equal(t, "a\n\nb", got)

	got, err = FlattenSystem(nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestFromAnthropicTextAndImage(t *testing.T) {
	t.Parallel()

	req := &apicompat.AnthropicRequest{
		Messages: []apicompat.AnthropicMessage{{
			Role: "user",
			Content: rawJSON(t, `[
				{"type":"text","text":"look"},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"QUJD"}}
			]`),
		}},
	}

	msgs, err := FromAnthropic(req)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Equal(t, "user", msgs[0].Role)
	require.Equal(t, "look", msgs[0].Text)
	require.Len(t, msgs[0].Images, 1)
	require.Equal(t, "png", msgs[0].Images[0].Format)
	require.Equal(t, "QUJD", msgs[0].Images[0].Data)
}

func TestFromAnthropicRejectsNonBase64Image(t *testing.T) {
	t.Parallel()

	req := &apicompat.AnthropicRequest{
		Messages: []apicompat.AnthropicMessage{{
			Role:    "user",
			Content: rawJSON(t, `[{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}]`),
		}},
	}

	_, err := FromAnthropic(req)
	require.ErrorIs(t, err, ErrUnsupportedImageSource)
}

// TestFromAnthropicAggregatesParallelToolResults 是本任务最重要的测试。
// Claude Code 并行调用工具时，多个 tool_result 会出现在同一条 user message 里。
// 丢任何一个都会让上游看到不完整的工具轮次。
func TestFromAnthropicAggregatesParallelToolResults(t *testing.T) {
	t.Parallel()

	req := &apicompat.AnthropicRequest{
		Messages: []apicompat.AnthropicMessage{{
			Role: "user",
			Content: rawJSON(t, `[
				{"type":"tool_result","tool_use_id":"tu_1","content":"first"},
				{"type":"tool_result","tool_use_id":"tu_2","content":[{"type":"text","text":"second"}]},
				{"type":"tool_result","tool_use_id":"tu_3","content":"boom","is_error":true}
			]`),
		}},
	}

	msgs, err := FromAnthropic(req)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Len(t, msgs[0].ToolResults, 3, "三个 tool_result 必须全部保留")
	require.Equal(t, "tu_1", msgs[0].ToolResults[0].ToolUseID)
	require.Equal(t, "first", msgs[0].ToolResults[0].Text)
	require.Equal(t, "second", msgs[0].ToolResults[1].Text)
	require.True(t, msgs[0].ToolResults[2].IsError)
}

func TestFromAnthropicToolUse(t *testing.T) {
	t.Parallel()

	req := &apicompat.AnthropicRequest{
		Messages: []apicompat.AnthropicMessage{{
			Role: "assistant",
			Content: rawJSON(t, `[
				{"type":"text","text":"calling"},
				{"type":"tool_use","id":"tu_1","name":"Read","input":{"path":"/a"}}
			]`),
		}},
	}

	msgs, err := FromAnthropic(req)
	require.NoError(t, err)
	require.Len(t, msgs[0].ToolCalls, 1)
	require.Equal(t, "tu_1", msgs[0].ToolCalls[0].ID)
	require.Equal(t, "Read", msgs[0].ToolCalls[0].Name)
	require.JSONEq(t, `{"path":"/a"}`, string(msgs[0].ToolCalls[0].Input))
}

func TestMergeAdjacentJoinsSameRole(t *testing.T) {
	t.Parallel()

	in := []Msg{
		{Role: "user", Text: "a"},
		{Role: "user", Text: "b", ToolResults: []ToolResult{{ToolUseID: "t1", Text: "r"}}},
		{Role: "assistant", Text: "c"},
	}

	out := MergeAdjacent(in)
	require.Len(t, out, 2)
	require.Equal(t, "a\n\nb", out[0].Text)
	require.Len(t, out[0].ToolResults, 1, "合并时 toolResults 不得丢失")
	require.Equal(t, "assistant", out[1].Role)
}

func TestEnsureFirstIsUserDropsLeadingAssistant(t *testing.T) {
	t.Parallel()

	out := EnsureFirstIsUser([]Msg{
		{Role: "assistant", Text: "stray"},
		{Role: "user", Text: "hi"},
	})
	require.Len(t, out, 1)
	require.Equal(t, "user", out[0].Role)

	// 全是 assistant 时返回空，由上层决定如何兜底。
	require.Empty(t, EnsureFirstIsUser([]Msg{{Role: "assistant", Text: "x"}}))
}

func TestEnsureAlternatingInsertsFiller(t *testing.T) {
	t.Parallel()

	// MergeAdjacent 之后理论上不会有连续同角色，但防御性地保证不变式。
	out := EnsureAlternating([]Msg{
		{Role: "user", Text: "a"},
		{Role: "user", Text: "b"},
	})

	require.Len(t, out, 3)
	require.Equal(t, "user", out[0].Role)
	require.Equal(t, "assistant", out[1].Role)
	require.Equal(t, "user", out[2].Role)
}

func TestStripToolContentRemovesCallsAndResults(t *testing.T) {
	t.Parallel()

	out := StripToolContent([]Msg{{
		Role:        "user",
		Text:        "keep",
		ToolCalls:   []ToolCall{{ID: "a"}},
		ToolResults: []ToolResult{{ToolUseID: "b"}},
	}})

	require.Equal(t, "keep", out[0].Text)
	require.Empty(t, out[0].ToolCalls)
	require.Empty(t, out[0].ToolResults)
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd backend && go test ./internal/pkg/kiro/ -run 'TestFlattenSystem|TestFromAnthropic|TestMerge|TestEnsure|TestStrip' -v
```

Expected: FAIL —— `undefined: FlattenSystem` 等。

- [ ] **Step 3: 实现 `messages.go`**

```go
package kiro

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

var (
	// ErrUnsupportedImageSource 表示图片不是 base64 内联字节。
	// Kiro 只接受内联字节；URL 形态若由网关代下载会引入 SSRF 面，因此直接拒绝。
	ErrUnsupportedImageSource = errors.New("kiro: only base64 image sources are supported")
	// ErrUnsupportedBlock 表示出现了 Kiro 无对应语义的内容块（如 document/PDF）。
	ErrUnsupportedBlock = errors.New("kiro: unsupported content block")
)

// Image 是 Kiro 形态的图片：格式名 + base64 字节。
type Image struct {
	Format string
	Data   string
}

// ToolCall 是助手发起的一次工具调用。
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolResult 是用户回传的一个工具结果。
type ToolResult struct {
	ToolUseID string
	Text      string
	IsError   bool
}

// Msg 是协议无关的中间消息形态，仅供本包内部使用。
type Msg struct {
	Role        string
	Text        string
	Images      []Image
	ToolCalls   []ToolCall
	ToolResults []ToolResult
}

// FlattenSystem 把 Anthropic 的 system 字段（string 或 content block 数组）
// 压平成单个字符串。Kiro 没有 system 角色，调用方需把结果拼进首条 user message。
func FlattenSystem(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}

	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString, nil
	}

	var blocks []apicompat.AnthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", fmt.Errorf("kiro: decode system: %w", err)
	}

	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

// FromAnthropic 把 Anthropic 消息数组转成中间形态。
// 不做角色规整 —— 那是 MergeAdjacent / EnsureFirstIsUser / EnsureAlternating 的职责。
func FromAnthropic(req *apicompat.AnthropicRequest) ([]Msg, error) {
	if req == nil {
		return nil, nil
	}

	msgs := make([]Msg, 0, len(req.Messages))
	for i, m := range req.Messages {
		msg, err := convertMessage(m)
		if err != nil {
			return nil, fmt.Errorf("kiro: message[%d]: %w", i, err)
		}
		msgs = append(msgs, msg)
	}
	return msgs, nil
}

func convertMessage(m apicompat.AnthropicMessage) (Msg, error) {
	out := Msg{Role: m.Role}

	// content 可能是裸字符串。
	var asString string
	if err := json.Unmarshal(m.Content, &asString); err == nil {
		out.Text = asString
		return out, nil
	}

	var blocks []apicompat.AnthropicContentBlock
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return Msg{}, fmt.Errorf("decode content: %w", err)
	}

	var texts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				texts = append(texts, b.Text)
			}

		case "thinking", "redacted_thinking":
			// Kiro 没有原生 reasoning，历史里的 thinking 块直接丢弃，
			// 不回传给上游（无 signature 的思考块回传只会引发校验问题）。

		case "image":
			img, err := convertImage(b.Source)
			if err != nil {
				return Msg{}, err
			}
			out.Images = append(out.Images, img)

		case "tool_use":
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:    b.ID,
				Name:  b.Name,
				Input: b.Input,
			})

		case "tool_result":
			text, err := flattenToolResultContent(b.Content)
			if err != nil {
				return Msg{}, err
			}
			out.ToolResults = append(out.ToolResults, ToolResult{
				ToolUseID: b.ToolUseID,
				Text:      text,
				IsError:   b.IsError,
			})

		default:
			return Msg{}, fmt.Errorf("%w: %q", ErrUnsupportedBlock, b.Type)
		}
	}

	out.Text = strings.Join(texts, "\n\n")
	return out, nil
}

func convertImage(src *apicompat.AnthropicImageSource) (Image, error) {
	if src == nil || src.Type != "base64" {
		return Image{}, ErrUnsupportedImageSource
	}

	format := src.MediaType
	if idx := strings.Index(format, "/"); idx >= 0 {
		format = format[idx+1:]
	}
	if format == "" {
		format = "jpeg"
	}

	return Image{Format: format, Data: src.Data}, nil
}

// flattenToolResultContent 把 tool_result 的 content（string 或 block 数组）压平成文本。
func flattenToolResultContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}

	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString, nil
	}

	var blocks []apicompat.AnthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", fmt.Errorf("decode tool_result content: %w", err)
	}

	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

// MergeAdjacent 合并连续同角色的消息。
// 文本用空行拼接，图片 / 工具调用 / 工具结果按序追加 —— 任何一项丢失都会让上游
// 看到不完整的工具轮次。
func MergeAdjacent(msgs []Msg) []Msg {
	if len(msgs) == 0 {
		return msgs
	}

	out := make([]Msg, 0, len(msgs))
	for _, m := range msgs {
		if len(out) == 0 || out[len(out)-1].Role != m.Role {
			out = append(out, m)
			continue
		}

		prev := &out[len(out)-1]
		switch {
		case prev.Text == "":
			prev.Text = m.Text
		case m.Text != "":
			prev.Text += "\n\n" + m.Text
		}
		prev.Images = append(prev.Images, m.Images...)
		prev.ToolCalls = append(prev.ToolCalls, m.ToolCalls...)
		prev.ToolResults = append(prev.ToolResults, m.ToolResults...)
	}
	return out
}

// EnsureFirstIsUser 丢弃开头的 assistant 消息 —— Kiro 要求会话以 user 开始。
func EnsureFirstIsUser(msgs []Msg) []Msg {
	for i, m := range msgs {
		if m.Role == "user" {
			return msgs[i:]
		}
	}
	return nil
}

// EnsureAlternating 保证 user/assistant 严格交替，必要时插入占位消息。
// MergeAdjacent 之后理论上不会触发，保留为防御性不变式。
func EnsureAlternating(msgs []Msg) []Msg {
	if len(msgs) < 2 {
		return msgs
	}

	out := make([]Msg, 0, len(msgs))
	out = append(out, msgs[0])
	for _, m := range msgs[1:] {
		if out[len(out)-1].Role == m.Role {
			filler := "assistant"
			if m.Role == "assistant" {
				filler = "user"
			}
			out = append(out, Msg{Role: filler, Text: "(continued)"})
		}
		out = append(out, m)
	}
	return out
}

// StripToolContent 清空所有工具调用与结果。
// 请求未声明 tools 时必须调用 —— 否则上游会因「引用了未声明的工具」而拒绝。
func StripToolContent(msgs []Msg) []Msg {
	out := make([]Msg, len(msgs))
	copy(out, msgs)
	for i := range out {
		out[i].ToolCalls = nil
		out[i].ToolResults = nil
	}
	return out
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd backend && go test ./internal/pkg/kiro/ -v
```

Expected: 全部 PASS。特别确认 `TestFromAnthropicAggregatesParallelToolResults` 通过。

- [ ] **Step 5: 提交**

```bash
cd backend && gofmt -w internal/pkg/kiro/ && go vet ./internal/pkg/kiro/
git add backend/internal/pkg/kiro/messages.go backend/internal/pkg/kiro/messages_test.go
git commit -m "feat(kiro): 消息规整链与 Anthropic 内容块转换"
```

---

> **计划文档状态：** Task 1-4 已完整展开（含可直接运行的测试与实现代码）。
> Task 5-20 的接口契约见下方「后续任务概览」，按同样详细程度逐组补齐 ——
> 一次补一组，避免一份超长文档中的接口签名在编写过程中互相漂移。

## 后续任务概览（待补齐为完整步骤）

### A 组剩余

| Task | 交付物 | 关键接口 |
|---|---|---|
| 5 | `request.go` 请求构造 | `type Options struct{ModelID, ConversationID, ProfileArn, Origin string; FakeThinking bool; FakeThinkingMaxTokens, ToolDescMaxLen int}`；`func BuildRequest(*apicompat.AnthropicRequest, Options) (*Request, error)`。按 spec §6.1 九步流水线，消费 Task 3 的 `SanitizeSchema` 与 Task 4 的全部导出函数 |
| 6 | `stream.go` 流式转换 | `func NewStreamTranslator(model string) *StreamTranslator`；`func (*StreamTranslator) Feed([]byte) ([]apicompat.AnthropicStreamEvent, error)`；`func (*StreamTranslator) Finalize() []apicompat.AnthropicStreamEvent`；`func (*StreamTranslator) Usage() apicompat.AnthropicUsage`；`func (*StreamTranslator) Credits() float64`。`toolUseEvent.Input` 分片累积 → `input_json_delta` |
| 7 | `tokens.go` token 估算 | `func EstimateText(string) int`；`func EstimateRequestInput(*apicompat.AnthropicRequest) int`。移植 `Kiro-Go/proxy/token_estimator.go` 的分字符类加权公式（ascii/4.5 + digit/2 + symbol/1.5 + 非 ASCII/1.5） |
| 8 | `models.go` + `endpoints.go` + `errors.go` | `func MapModel(string) string`；`func DefaultModels() []string`；`type Endpoint struct{URL, Origin, AmzTarget, Name string}`；`func EndpointsFor(isAPIKey bool, region string) []Endpoint`；`type Signal int` + `func Classify(status int, body []byte) Signal` —— **必测 `INVALID_MODEL_ID` 归类为网络问题（不标记账号故障）、400 归类为不可重试不可转移** |


### B 组：凭证与授权

| Task | 交付物 |
|---|---|
| 9 | `Account` 的 kiro credentials 访问器（`auth_method` / `profile_arn` / `machine_id` / `region` / `api_key`），`machine_id` 缺失时生成并持久化 |
| 10 | `kiro_token_refresher.go` 三条刷新路径；**刷新响应的 `profileArn` 必须回写** |
| 11 | `kiro_oauth_service.go`：IdC 的 `client/register` + PKCE + `/authorize`；Builder ID 的 device_code 轮询；会话暂存用 `internal/pkg/redissession`（非进程内存，因为自建回调页在多副本下会跨副本） |
| 12 | 注册进 `token_refresh_service.go:139` 的 registrations 表；`admin/kiro_oauth_handler.go` + 回调路由 |

### C 组：平台与网关

| Task | 交付物 |
|---|---|
| 13 | `PlatformKiro` 提升为一等常量 + `AllowedQuotaPlatforms` + `migrations/234_kiro_platform.sql`（**必须同 PR**，见 spec §4.4 的生产事故） |
| 14 | `kiro_gateway_service.go` 转发主流程 + 端点 fallback |
| 15 | 错误分类接入调度：403 刷新重试、429 换端点、credits 耗尽写 `model_rate_limits["KiroCredits"]` |
| 16 | `routes/gateway.go:195` 的 `/v1/messages` kiro 分支 + handler + wire provider set |

### D 组：额度与计费

| Task | 交付物 |
|---|---|
| 17 | `kiro_quota_fetcher.go` 实现 `CanFetch` / `FetchQuota` / `GetProxyURL`，调 `getUsageLimits` |
| 18 | 计费接入：cache token 用 `meteringEvent` 真实值，input/output 用估算，`billing_mode="token"` |

### E 组：前端

| Task | 交付物 |
|---|---|
| 19 | 账号表单（四种 auth_method 分支）+ 授权向导（IdC 跳转 / device code 展示） |
| 20 | 额度展示 + 分组平台选项 |
