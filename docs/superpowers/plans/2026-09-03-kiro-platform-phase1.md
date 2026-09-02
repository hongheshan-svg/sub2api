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

### Task 5: 请求构造（Anthropic → Kiro conversationState）

**Files:**
- Create: `backend/internal/pkg/kiro/request.go`
- Test: `backend/internal/pkg/kiro/request_test.go`

**Interfaces:**
- Consumes: Task 3 的 `SanitizeSchema`；Task 4 的 `Msg`、`Image`、`ToolCall`、`ToolResult`、`FlattenSystem`、`FromAnthropic`、`MergeAdjacent`、`EnsureFirstIsUser`、`EnsureAlternating`、`StripToolContent`
- Produces:
  - `type Request struct { ConversationState ConversationState; ProfileArn string }`
  - `type ConversationState struct { ChatTriggerType, ConversationID string; CurrentMessage CurrentMessage; History []HistoryEntry }`
  - `type CurrentMessage struct { UserInputMessage UserInputMessage }`
  - `type UserInputMessage struct { Content, ModelID, Origin string; Images []KiroImage; UserInputMessageContext *UserInputMessageContext }`
  - `type KiroImage struct { Format string; Source ImageSource }`、`type ImageSource struct { Bytes string }`
  - `type UserInputMessageContext struct { Tools []Tool; ToolResults []KiroToolResult }`
  - `type Tool struct { ToolSpecification ToolSpecification }`、`type ToolSpecification struct { Name, Description string; InputSchema InputSchema }`、`type InputSchema struct { JSON map[string]any }`
  - `type KiroToolResult struct { ToolUseID, Status string; Content []ToolResultContent }`、`type ToolResultContent struct { Text string }`
  - `type HistoryEntry struct { UserInputMessage *UserInputMessage; AssistantResponseMessage *AssistantResponseMessage }`
  - `type AssistantResponseMessage struct { Content string; ToolUses []KiroToolUse }`、`type KiroToolUse struct { Name string; Input json.RawMessage; ToolUseID string }`
  - `type Options struct { ModelID, ConversationID, ProfileArn, Origin string; FakeThinking bool; FakeThinkingMaxTokens, ToolDescMaxLen int }`
  - `func BuildRequest(req *apicompat.AnthropicRequest, opts Options) (*Request, error)`
  - `var ErrNoMessages`

**流水线（spec §6.1，实现必须严格按此顺序）：**

```
1. 模型解析        opts.ModelID 已由调用方解析好，直接用
2. 工具预处理      description > ToolDescMaxLen → 移入 system prompt；InputSchema 走 SanitizeSchema
3. system 拼接     FlattenSystem → 加上工具文档 → 拼到第一条 user message 之前
4. 消息规整链      无 tools 时 StripToolContent → MergeAdjacent → EnsureFirstIsUser → EnsureAlternating
5. history 构造    除最后一条外 → []HistoryEntry
6. currentMessage  最后一条；若为 assistant → 移入 history，content 顶替为 "Continue"
7. 假思考注入      opts.FakeThinking 且 current 是 user 时注入
8. images/toolResults → UserInputMessageContext
9. 固定字段        Origin、ChatTriggerType="MANUAL"、ConversationID、ProfileArn
```

**注意**：`Origin` 由调用方按端点决定（`AI_EDITOR` 或 `KIRO_CLI`），
`opts.Origin` 为空时默认 `AI_EDITOR`。

- [ ] **Step 1: 写失败测试**

创建 `backend/internal/pkg/kiro/request_test.go`：

```go
package kiro

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

func baseOpts() Options {
	return Options{
		ModelID:        "claude-sonnet-4.6",
		ConversationID: "conv-1",
		ProfileArn:     "arn:aws:codewhisperer:::profile/ABC",
		ToolDescMaxLen: 10000,
	}
}

func TestBuildRequestFixedFields(t *testing.T) {
	t.Parallel()

	req := &apicompat.AnthropicRequest{
		Messages: []apicompat.AnthropicMessage{
			{Role: "user", Content: rawJSON(t, `"hello"`)},
		},
	}

	out, err := BuildRequest(req, baseOpts())
	require.NoError(t, err)
	require.Equal(t, "MANUAL", out.ConversationState.ChatTriggerType)
	require.Equal(t, "conv-1", out.ConversationState.ConversationID)
	require.Equal(t, "arn:aws:codewhisperer:::profile/ABC", out.ProfileArn)

	um := out.ConversationState.CurrentMessage.UserInputMessage
	require.Equal(t, "hello", um.Content)
	require.Equal(t, "claude-sonnet-4.6", um.ModelID)
	require.Equal(t, "AI_EDITOR", um.Origin)
	require.Empty(t, out.ConversationState.History)
}

// TestBuildRequestSystemPrependedToFirstUser 覆盖 spec §6.3 的头号有损项：
// Kiro 没有 system 角色，system 必须拼进第一条 user message。
func TestBuildRequestSystemPrependedToFirstUser(t *testing.T) {
	t.Parallel()

	req := &apicompat.AnthropicRequest{
		System: rawJSON(t, `"SYSTEM RULES"`),
		Messages: []apicompat.AnthropicMessage{
			{Role: "user", Content: rawJSON(t, `"first"`)},
			{Role: "assistant", Content: rawJSON(t, `"ack"`)},
			{Role: "user", Content: rawJSON(t, `"second"`)},
		},
	}

	out, err := BuildRequest(req, baseOpts())
	require.NoError(t, err)

	// 有 history 时，system 拼到 history 里的第一条 user，而不是 current。
	require.Len(t, out.ConversationState.History, 2)
	firstUser := out.ConversationState.History[0].UserInputMessage
	require.NotNil(t, firstUser)
	require.True(t, strings.HasPrefix(firstUser.Content, "SYSTEM RULES"))
	require.Contains(t, firstUser.Content, "first")

	// current 保持原样。
	require.Equal(t, "second", out.ConversationState.CurrentMessage.UserInputMessage.Content)
}

func TestBuildRequestSystemGoesToCurrentWhenNoHistory(t *testing.T) {
	t.Parallel()

	req := &apicompat.AnthropicRequest{
		System: rawJSON(t, `"SYSTEM RULES"`),
		Messages: []apicompat.AnthropicMessage{
			{Role: "user", Content: rawJSON(t, `"only"`)},
		},
	}

	out, err := BuildRequest(req, baseOpts())
	require.NoError(t, err)
	require.Empty(t, out.ConversationState.History)

	content := out.ConversationState.CurrentMessage.UserInputMessage.Content
	require.True(t, strings.HasPrefix(content, "SYSTEM RULES"))
	require.Contains(t, content, "only")
}

// TestBuildRequestTrailingAssistantBecomesContinue 覆盖 assistant prefill 的有损转换。
func TestBuildRequestTrailingAssistantBecomesContinue(t *testing.T) {
	t.Parallel()

	req := &apicompat.AnthropicRequest{
		Messages: []apicompat.AnthropicMessage{
			{Role: "user", Content: rawJSON(t, `"q"`)},
			{Role: "assistant", Content: rawJSON(t, `"partial answer"`)},
		},
	}

	out, err := BuildRequest(req, baseOpts())
	require.NoError(t, err)
	require.Equal(t, "Continue", out.ConversationState.CurrentMessage.UserInputMessage.Content)

	last := out.ConversationState.History[len(out.ConversationState.History)-1]
	require.NotNil(t, last.AssistantResponseMessage)
	require.Equal(t, "partial answer", last.AssistantResponseMessage.Content)
}

func TestBuildRequestToolsConvertedAndSanitized(t *testing.T) {
	t.Parallel()

	req := &apicompat.AnthropicRequest{
		Messages: []apicompat.AnthropicMessage{
			{Role: "user", Content: rawJSON(t, `"go"`)},
		},
		Tools: []apicompat.AnthropicTool{{
			Name:        "Read",
			Description: "read a file",
			InputSchema: rawJSON(t, `{"type":"object","additionalProperties":false,"required":[],"properties":{"p":{"type":"string"}}}`),
		}},
	}

	out, err := BuildRequest(req, baseOpts())
	require.NoError(t, err)

	ctx := out.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	require.NotNil(t, ctx)
	require.Len(t, ctx.Tools, 1)

	spec := ctx.Tools[0].ToolSpecification
	require.Equal(t, "Read", spec.Name)
	require.Equal(t, "read a file", spec.Description)
	require.NotContains(t, spec.InputSchema.JSON, "additionalProperties", "schema 必须经过 SanitizeSchema")
	require.NotContains(t, spec.InputSchema.JSON, "required")
}

func TestBuildRequestLongToolDescriptionMovedToSystem(t *testing.T) {
	t.Parallel()

	longDesc := strings.Repeat("x", 200)
	opts := baseOpts()
	opts.ToolDescMaxLen = 50

	req := &apicompat.AnthropicRequest{
		Messages: []apicompat.AnthropicMessage{
			{Role: "user", Content: rawJSON(t, `"go"`)},
		},
		Tools: []apicompat.AnthropicTool{{
			Name:        "Bash",
			Description: longDesc,
			InputSchema: rawJSON(t, `{"type":"object"}`),
		}},
	}

	out, err := BuildRequest(req, opts)
	require.NoError(t, err)

	content := out.ConversationState.CurrentMessage.UserInputMessage.Content
	require.Contains(t, content, "## Tool: Bash", "长描述应移入 system prompt")
	require.Contains(t, content, longDesc)

	spec := out.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools[0].ToolSpecification
	require.Contains(t, spec.Description, "Full documentation in system prompt")
	require.NotContains(t, spec.Description, longDesc)
}

// TestBuildRequestStripsToolContentWhenNoTools 覆盖：请求未声明 tools 时，
// 历史里的工具调用/结果必须清空，否则上游会因引用未声明的工具而拒绝。
func TestBuildRequestStripsToolContentWhenNoTools(t *testing.T) {
	t.Parallel()

	req := &apicompat.AnthropicRequest{
		Messages: []apicompat.AnthropicMessage{
			{Role: "user", Content: rawJSON(t, `"q"`)},
			{Role: "assistant", Content: rawJSON(t, `[{"type":"tool_use","id":"tu_1","name":"Read","input":{}}]`)},
			{Role: "user", Content: rawJSON(t, `[{"type":"tool_result","tool_use_id":"tu_1","content":"r"}]`)},
		},
	}

	out, err := BuildRequest(req, baseOpts())
	require.NoError(t, err)

	for _, h := range out.ConversationState.History {
		if h.AssistantResponseMessage != nil {
			require.Empty(t, h.AssistantResponseMessage.ToolUses)
		}
		if h.UserInputMessage != nil && h.UserInputMessage.UserInputMessageContext != nil {
			require.Empty(t, h.UserInputMessage.UserInputMessageContext.ToolResults)
		}
	}
	ctx := out.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if ctx != nil {
		require.Empty(t, ctx.ToolResults)
	}
}

func TestBuildRequestToolResultsMappedWithStatus(t *testing.T) {
	t.Parallel()

	req := &apicompat.AnthropicRequest{
		Messages: []apicompat.AnthropicMessage{
			{Role: "user", Content: rawJSON(t, `"q"`)},
			{Role: "assistant", Content: rawJSON(t, `[{"type":"tool_use","id":"tu_1","name":"Read","input":{}}]`)},
			{Role: "user", Content: rawJSON(t, `[
				{"type":"tool_result","tool_use_id":"tu_1","content":"ok"},
				{"type":"tool_result","tool_use_id":"tu_2","content":"bad","is_error":true}
			]`)},
		},
		Tools: []apicompat.AnthropicTool{{Name: "Read", InputSchema: rawJSON(t, `{"type":"object"}`)}},
	}

	out, err := BuildRequest(req, baseOpts())
	require.NoError(t, err)

	ctx := out.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	require.NotNil(t, ctx)
	require.Len(t, ctx.ToolResults, 2)
	require.Equal(t, "success", ctx.ToolResults[0].Status)
	require.Equal(t, "ok", ctx.ToolResults[0].Content[0].Text)
	require.Equal(t, "error", ctx.ToolResults[1].Status)
}

func TestBuildRequestImagesMapped(t *testing.T) {
	t.Parallel()

	req := &apicompat.AnthropicRequest{
		Messages: []apicompat.AnthropicMessage{{
			Role: "user",
			Content: rawJSON(t, `[
				{"type":"text","text":"see"},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"QUJD"}}
			]`),
		}},
	}

	out, err := BuildRequest(req, baseOpts())
	require.NoError(t, err)

	imgs := out.ConversationState.CurrentMessage.UserInputMessage.Images
	require.Len(t, imgs, 1)
	require.Equal(t, "png", imgs[0].Format)
	require.Equal(t, "QUJD", imgs[0].Source.Bytes)
}

func TestBuildRequestEmptyContentBecomesContinue(t *testing.T) {
	t.Parallel()

	req := &apicompat.AnthropicRequest{
		Messages: []apicompat.AnthropicMessage{
			{Role: "user", Content: rawJSON(t, `""`)},
		},
	}

	out, err := BuildRequest(req, baseOpts())
	require.NoError(t, err)
	require.Equal(t, "Continue", out.ConversationState.CurrentMessage.UserInputMessage.Content)
}

func TestBuildRequestFakeThinkingInjection(t *testing.T) {
	t.Parallel()

	req := &apicompat.AnthropicRequest{
		Messages: []apicompat.AnthropicMessage{
			{Role: "user", Content: rawJSON(t, `"solve it"`)},
		},
	}

	opts := baseOpts()
	opts.FakeThinking = true
	opts.FakeThinkingMaxTokens = 4000

	out, err := BuildRequest(req, opts)
	require.NoError(t, err)

	content := out.ConversationState.CurrentMessage.UserInputMessage.Content
	require.Contains(t, content, "<thinking_mode>enabled</thinking_mode>")
	require.Contains(t, content, "<max_thinking_length>4000</max_thinking_length>")
	require.Contains(t, content, "solve it")

	// 关闭时不得注入。
	opts.FakeThinking = false
	out, err = BuildRequest(req, opts)
	require.NoError(t, err)
	require.NotContains(t, out.ConversationState.CurrentMessage.UserInputMessage.Content, "<thinking_mode>")
}

func TestBuildRequestNoUsableMessages(t *testing.T) {
	t.Parallel()

	_, err := BuildRequest(&apicompat.AnthropicRequest{}, baseOpts())
	require.ErrorIs(t, err, ErrNoMessages)

	// 全是 assistant → EnsureFirstIsUser 清空 → 同样是 ErrNoMessages。
	_, err = BuildRequest(&apicompat.AnthropicRequest{
		Messages: []apicompat.AnthropicMessage{{Role: "assistant", Content: rawJSON(t, `"x"`)}},
	}, baseOpts())
	require.ErrorIs(t, err, ErrNoMessages)
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd backend && go test ./internal/pkg/kiro/ -run TestBuildRequest -v
```

Expected: FAIL —— `undefined: BuildRequest` / `undefined: Options`。

- [ ] **Step 3: 实现 `request.go`**

```go
package kiro

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

// ErrNoMessages 表示规整后没有任何可发送的消息。
var ErrNoMessages = errors.New("kiro: no usable messages after normalization")

// defaultOrigin 是 Kiro IDE 路径的 origin；API Key 路径由调用方传 KIRO_CLI。
const defaultOrigin = "AI_EDITOR"

// continuePlaceholder 用于顶替空内容或 assistant 结尾的场景。
// Kiro 不接受空 content，也没有 assistant prefill 语义。
const continuePlaceholder = "Continue"

// Request 是 generateAssistantResponse 的请求体。
type Request struct {
	ConversationState ConversationState `json:"conversationState"`
	ProfileArn        string            `json:"profileArn,omitempty"`
}

// ConversationState 承载完整会话。
type ConversationState struct {
	ChatTriggerType string         `json:"chatTriggerType"`
	ConversationID  string         `json:"conversationId"`
	CurrentMessage  CurrentMessage `json:"currentMessage"`
	History         []HistoryEntry `json:"history,omitempty"`
}

// CurrentMessage 是本轮要发送的消息。
type CurrentMessage struct {
	UserInputMessage UserInputMessage `json:"userInputMessage"`
}

// UserInputMessage 是 Kiro 的用户消息形态。注意它没有 system 角色，
// 也没有 temperature / top_p / max_tokens 等采样参数的槽位。
type UserInputMessage struct {
	Content                 string                   `json:"content"`
	ModelID                 string                   `json:"modelId"`
	Origin                  string                   `json:"origin"`
	Images                  []KiroImage              `json:"images,omitempty"`
	UserInputMessageContext *UserInputMessageContext `json:"userInputMessageContext,omitempty"`
}

// KiroImage 是 Kiro 的图片形态：格式名 + base64 字节。
type KiroImage struct {
	Format string      `json:"format"`
	Source ImageSource `json:"source"`
}

// ImageSource 承载 base64 字节。
type ImageSource struct {
	Bytes string `json:"bytes"`
}

// UserInputMessageContext 携带工具声明与工具结果。
type UserInputMessageContext struct {
	Tools       []Tool           `json:"tools,omitempty"`
	ToolResults []KiroToolResult `json:"toolResults,omitempty"`
}

// Tool 是一条工具声明。
type Tool struct {
	ToolSpecification ToolSpecification `json:"toolSpecification"`
}

// ToolSpecification 是工具的名称/描述/入参 schema。
type ToolSpecification struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}

// InputSchema 包住 JSON Schema。
type InputSchema struct {
	JSON map[string]any `json:"json"`
}

// KiroToolResult 是一个工具执行结果。
type KiroToolResult struct {
	ToolUseID string              `json:"toolUseId"`
	Status    string              `json:"status"`
	Content   []ToolResultContent `json:"content"`
}

// ToolResultContent 是工具结果的一段文本。
type ToolResultContent struct {
	Text string `json:"text"`
}

// HistoryEntry 是历史里的一条消息，两个字段互斥。
type HistoryEntry struct {
	UserInputMessage         *UserInputMessage         `json:"userInputMessage,omitempty"`
	AssistantResponseMessage *AssistantResponseMessage `json:"assistantResponseMessage,omitempty"`
}

// AssistantResponseMessage 是历史里的助手消息。
type AssistantResponseMessage struct {
	Content  string        `json:"content"`
	ToolUses []KiroToolUse `json:"toolUses,omitempty"`
}

// KiroToolUse 是历史里的一次工具调用。
type KiroToolUse struct {
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"toolUseId"`
}

// Options 控制请求构造的可变部分。
type Options struct {
	// ModelID 是已解析好的 Kiro 上游模型名。
	ModelID string
	// ConversationID 必须与粘性会话一致；换账号时调用方需重新生成。
	ConversationID string
	// ProfileArn 对 API Key 账号应留空。
	ProfileArn string
	// Origin 为空时默认 AI_EDITOR；API Key 路径传 KIRO_CLI。
	Origin string
	// FakeThinking 开启后向 prompt 注入思考指令，并由 StreamTranslator 剥离。
	FakeThinking bool
	// FakeThinkingMaxTokens 是注入指令里声明的思考预算。
	FakeThinkingMaxTokens int
	// ToolDescMaxLen 超过此长度的工具描述移入 system prompt。
	ToolDescMaxLen int
}

// BuildRequest 把 Anthropic 请求转换成 Kiro 的 conversationState。
// 转换是有损的，完整清单见设计文档 §6.3。
func BuildRequest(req *apicompat.AnthropicRequest, opts Options) (*Request, error) {
	if req == nil {
		return nil, ErrNoMessages
	}

	// 1-2. 工具预处理：长描述移入 system，schema 清洗。
	tools, toolDocs := processTools(req.Tools, opts.ToolDescMaxLen)

	// 3. system 拼接。
	systemText, err := FlattenSystem(req.System)
	if err != nil {
		return nil, err
	}
	if toolDocs != "" {
		if systemText == "" {
			systemText = strings.TrimSpace(toolDocs)
		} else {
			systemText += toolDocs
		}
	}

	// 4. 消息规整链。
	msgs, err := FromAnthropic(req)
	if err != nil {
		return nil, err
	}
	if len(tools) == 0 {
		msgs = StripToolContent(msgs)
	}
	msgs = MergeAdjacent(msgs)
	msgs = EnsureFirstIsUser(msgs)
	msgs = EnsureAlternating(msgs)
	if len(msgs) == 0 {
		return nil, ErrNoMessages
	}

	origin := opts.Origin
	if origin == "" {
		origin = defaultOrigin
	}

	// 5. history 构造（除最后一条外）。system 拼到 history 首条 user。
	historyMsgs := msgs[:len(msgs)-1]
	current := msgs[len(msgs)-1]

	if systemText != "" && len(historyMsgs) > 0 {
		for i := range historyMsgs {
			if historyMsgs[i].Role == "user" {
				historyMsgs[i].Text = joinSystem(systemText, historyMsgs[i].Text)
				break
			}
		}
	}

	history := buildHistory(historyMsgs, opts.ModelID, origin)

	// 6. current message；assistant 结尾时移入 history 并顶替为 Continue。
	currentContent := current.Text
	if systemText != "" && len(historyMsgs) == 0 {
		currentContent = joinSystem(systemText, currentContent)
	}

	if current.Role == "assistant" {
		history = append(history, HistoryEntry{
			AssistantResponseMessage: assistantEntry(current, currentContent),
		})
		current = Msg{Role: "user"}
		currentContent = continuePlaceholder
	}

	if strings.TrimSpace(currentContent) == "" {
		currentContent = continuePlaceholder
	}

	// 7. 假思考注入。
	if opts.FakeThinking && current.Role == "user" {
		currentContent = injectThinking(currentContent, opts.FakeThinkingMaxTokens)
	}

	// 8. images / toolResults / tools。
	userInput := UserInputMessage{
		Content: currentContent,
		ModelID: opts.ModelID,
		Origin:  origin,
		Images:  toKiroImages(current.Images),
	}
	if ctx := buildContext(tools, current.ToolResults); ctx != nil {
		userInput.UserInputMessageContext = ctx
	}

	// 9. 固定字段。
	out := &Request{
		ConversationState: ConversationState{
			ChatTriggerType: "MANUAL",
			ConversationID:  opts.ConversationID,
			CurrentMessage:  CurrentMessage{UserInputMessage: userInput},
			History:         history,
		},
		ProfileArn: opts.ProfileArn,
	}
	return out, nil
}

func joinSystem(system, content string) string {
	if content == "" {
		return system
	}
	return system + "\n\n" + content
}

// processTools 清洗 schema，并把超长描述移入 system prompt 文档段。
func processTools(tools []apicompat.AnthropicTool, maxLen int) ([]Tool, string) {
	if len(tools) == 0 {
		return nil, ""
	}

	out := make([]Tool, 0, len(tools))
	var docs []string

	for _, tool := range tools {
		var schema map[string]any
		if len(tool.InputSchema) > 0 {
			// 解析失败时退化为空对象，好过让上游 400。
			_ = json.Unmarshal(tool.InputSchema, &schema)
		}

		desc := tool.Description
		if desc == "" {
			desc = "Tool: " + tool.Name
		}
		if maxLen > 0 && len(desc) > maxLen {
			docs = append(docs, fmt.Sprintf("## Tool: %s\n\n%s", tool.Name, desc))
			desc = fmt.Sprintf("[Full documentation in system prompt under '## Tool: %s']", tool.Name)
		}

		out = append(out, Tool{ToolSpecification: ToolSpecification{
			Name:        tool.Name,
			Description: desc,
			InputSchema: InputSchema{JSON: SanitizeSchema(schema)},
		}})
	}

	var toolDocs string
	if len(docs) > 0 {
		toolDocs = "\n\n---\n# Tool Documentation\n\n" + strings.Join(docs, "\n\n---\n\n")
	}
	return out, toolDocs
}

func buildHistory(msgs []Msg, modelID, origin string) []HistoryEntry {
	if len(msgs) == 0 {
		return nil
	}

	history := make([]HistoryEntry, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "assistant" {
			history = append(history, HistoryEntry{
				AssistantResponseMessage: assistantEntry(m, m.Text),
			})
			continue
		}

		content := m.Text
		if content == "" {
			content = "(empty)"
		}
		entry := &UserInputMessage{
			Content: content,
			ModelID: modelID,
			Origin:  origin,
			Images:  toKiroImages(m.Images),
		}
		if ctx := buildContext(nil, m.ToolResults); ctx != nil {
			entry.UserInputMessageContext = ctx
		}
		history = append(history, HistoryEntry{UserInputMessage: entry})
	}
	return history
}

func assistantEntry(m Msg, content string) *AssistantResponseMessage {
	if content == "" {
		content = "(empty)"
	}
	out := &AssistantResponseMessage{Content: content}
	for _, tc := range m.ToolCalls {
		input := tc.Input
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		out.ToolUses = append(out.ToolUses, KiroToolUse{
			Name:      tc.Name,
			Input:     input,
			ToolUseID: tc.ID,
		})
	}
	return out
}

func buildContext(tools []Tool, results []ToolResult) *UserInputMessageContext {
	if len(tools) == 0 && len(results) == 0 {
		return nil
	}

	ctx := &UserInputMessageContext{Tools: tools}
	for _, r := range results {
		status := "success"
		if r.IsError {
			status = "error"
		}
		text := r.Text
		if text == "" {
			text = "(empty result)"
		}
		ctx.ToolResults = append(ctx.ToolResults, KiroToolResult{
			ToolUseID: r.ToolUseID,
			Status:    status,
			Content:   []ToolResultContent{{Text: text}},
		})
	}
	return ctx
}

func toKiroImages(images []Image) []KiroImage {
	if len(images) == 0 {
		return nil
	}
	out := make([]KiroImage, 0, len(images))
	for _, img := range images {
		out = append(out, KiroImage{
			Format: img.Format,
			Source: ImageSource{Bytes: img.Data},
		})
	}
	return out
}

// injectThinking 把思考指令注入到用户内容之前。
// Kiro 没有原生 reasoning，这是四份参考实现一致采用的替代方案。
func injectThinking(content string, maxTokens int) string {
	if maxTokens <= 0 {
		maxTokens = 4000
	}
	const instruction = `Think step by step. Make sure you fully understand what is being asked, ` +
		`consider multiple approaches, think about edge cases, challenge your assumptions, ` +
		`and verify your reasoning before concluding. ` +
		`Wrap your reasoning in <thinking>...</thinking> tags before your final response.`

	return fmt.Sprintf(
		"<thinking_mode>enabled</thinking_mode>\n<max_thinking_length>%d</max_thinking_length>\n<thinking_instruction>%s</thinking_instruction>\n\n%s",
		maxTokens, instruction, content)
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd backend && go test ./internal/pkg/kiro/ -v
```

Expected: 全部 PASS（13 个 `TestBuildRequest*` 加上此前任务的测试）。

- [ ] **Step 5: 提交**

```bash
cd backend && gofmt -w internal/pkg/kiro/ && go vet ./internal/pkg/kiro/
git add backend/internal/pkg/kiro/request.go backend/internal/pkg/kiro/request_test.go
git commit -m "feat(kiro): Anthropic 请求 → Kiro conversationState 构造"
```

---

### Task 6: token 估算

> **顺序说明**：本任务原排在 `stream.go` 之后，实际调换 —— `StreamTranslator.Usage()`
> 需要用本任务的 `EstimateText` 估算 output token，先做估算器可消除反向依赖。

**Files:**
- Create: `backend/internal/pkg/kiro/tokens.go`
- Test: `backend/internal/pkg/kiro/tokens_test.go`

**Interfaces:**
- Consumes: `apicompat.AnthropicRequest`；Task 4 的 `FlattenSystem`
- Produces:
  - `func EstimateText(s string) int`
  - `func EstimateRequestInput(req *apicompat.AnthropicRequest) int`

**背景（实现者必读）：** Kiro 的 `meteringEvent` **只给 credits 和真实的 cache token，
不给 input/output token**。本仓库全链路按 token 计费，因此 input/output 必须本地估算。
公式移植自 `Kiro-Go/proxy/token_estimator.go`，按字符类加权：

```
若 rune 数 < 5   → ceil(n / 3)，下限 1
否则            → ceil(ascii/4.5 + digits/2.0 + symbols/1.5 + nonASCII/1.5)，下限 1
```

字符分类：`r >= 0x80` 为 nonASCII；`'0'..'9'` 为 digits；
`'!'..'/'`、`':'..'@'`、`'['..'\``、`'{'..'~'` 为 symbols；其余为 ascii。

估算误差经验值 ±10-20%，这是既定的计费口径（设计文档 D4），不是缺陷。

- [ ] **Step 1: 写失败测试**

创建 `backend/internal/pkg/kiro/tokens_test.go`：

```go
package kiro

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

func TestEstimateTextEmpty(t *testing.T) {
	t.Parallel()
	require.Zero(t, EstimateText(""))
}

func TestEstimateTextShortStrings(t *testing.T) {
	t.Parallel()

	// 长度 < 5 走 ceil(n/3)，下限 1。
	require.Equal(t, 1, EstimateText("a"))
	require.Equal(t, 1, EstimateText("abc"))
	require.Equal(t, 2, EstimateText("abcd"))
}

func TestEstimateTextAsciiProse(t *testing.T) {
	t.Parallel()

	// 36 个普通 ascii 字符（含空格）→ ceil(36/4.5) = 8
	got := EstimateText("the quick brown fox jumps over lazyy")
	require.Equal(t, 8, got)
}

func TestEstimateTextCJKCostsMore(t *testing.T) {
	t.Parallel()

	// 非 ASCII 按 /1.5 计，中文比等长英文贵。
	cjk := EstimateText("中文字符测试内容一二三")
	ascii := EstimateText("aaaaaaaaaaa")
	require.Greater(t, cjk, ascii)
}

func TestEstimateTextSymbolsAndDigits(t *testing.T) {
	t.Parallel()

	// 12 个符号 → ceil(12/1.5) = 8
	require.Equal(t, 8, EstimateText("{}[]()<>!@#$"))
	// 12 个数字 → ceil(12/2) = 6
	require.Equal(t, 6, EstimateText("123456789012"))
}

func TestEstimateTextNeverZeroForNonEmpty(t *testing.T) {
	t.Parallel()
	require.GreaterOrEqual(t, EstimateText(" "), 1)
}

func TestEstimateRequestInputCoversSystemMessagesTools(t *testing.T) {
	t.Parallel()

	base := &apicompat.AnthropicRequest{
		Messages: []apicompat.AnthropicMessage{
			{Role: "user", Content: rawJSON(t, `"hello world this is a message"`)},
		},
	}
	withSystem := &apicompat.AnthropicRequest{
		System:   rawJSON(t, `"a fairly long system prompt goes here"`),
		Messages: base.Messages,
	}
	withTools := &apicompat.AnthropicRequest{
		Messages: base.Messages,
		Tools: []apicompat.AnthropicTool{{
			Name:        "Read",
			Description: "reads a file from disk and returns its contents",
			InputSchema: rawJSON(t, `{"type":"object","properties":{"path":{"type":"string"}}}`),
		}},
	}

	require.Greater(t, EstimateRequestInput(withSystem), EstimateRequestInput(base),
		"system 必须计入 input token")
	require.Greater(t, EstimateRequestInput(withTools), EstimateRequestInput(base),
		"工具声明必须计入 input token")
}

func TestEstimateRequestInputNil(t *testing.T) {
	t.Parallel()
	require.Zero(t, EstimateRequestInput(nil))
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd backend && go test ./internal/pkg/kiro/ -run TestEstimate -v
```

Expected: FAIL —— `undefined: EstimateText`。

- [ ] **Step 3: 实现 `tokens.go`**

```go
package kiro

import (
	"math"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

// EstimateText 估算一段文本的 token 数。
//
// Kiro 的 meteringEvent 只给 credits 和真实 cache token，不给 input/output token，
// 而本仓库按 token 计费，因此这两项必须本地估算。公式移植自
// Kiro-Go/proxy/token_estimator.go，按字符类加权。经验误差 ±10-20%。
func EstimateText(s string) int {
	if s == "" {
		return 0
	}

	runes := []rune(s)
	n := len(runes)
	if n == 0 {
		return 0
	}
	if n < 5 {
		if est := int(math.Ceil(float64(n) / 3.0)); est > 1 {
			return est
		}
		return 1
	}

	var ascii, digits, symbols, nonASCII int
	for _, r := range runes {
		switch {
		case r >= 0x80:
			nonASCII++
		case r >= '0' && r <= '9':
			digits++
		case (r >= '!' && r <= '/') || (r >= ':' && r <= '@') ||
			(r >= '[' && r <= '`') || (r >= '{' && r <= '~'):
			symbols++
		default:
			ascii++
		}
	}

	est := int(math.Ceil(
		float64(ascii)/4.5 +
			float64(digits)/2.0 +
			float64(symbols)/1.5 +
			float64(nonASCII)/1.5,
	))
	if est < 1 {
		return 1
	}
	return est
}

// EstimateRequestInput 估算整个请求的 input token：system + 全部消息 + 工具声明。
func EstimateRequestInput(req *apicompat.AnthropicRequest) int {
	if req == nil {
		return 0
	}

	total := 0

	if system, err := FlattenSystem(req.System); err == nil {
		total += EstimateText(system)
	}

	// 消息按原始 JSON 估算：内容块的结构本身也占 token，
	// 且这样不会因某条消息解析失败而整体归零。
	for _, m := range req.Messages {
		total += EstimateText(string(m.Content))
	}

	for _, tool := range req.Tools {
		total += EstimateText(tool.Name)
		total += EstimateText(tool.Description)
		total += EstimateText(string(tool.InputSchema))
	}

	return total
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd backend && go test ./internal/pkg/kiro/ -v
```

Expected: 全部 PASS。若 `TestEstimateTextAsciiProse` 的期望值与实现不符，
**以实现公式为准修正测试期望**（公式是移植来的既定口径，不要为了凑测试改公式）。

- [ ] **Step 5: 提交**

```bash
cd backend && gofmt -w internal/pkg/kiro/ && go vet ./internal/pkg/kiro/
git add backend/internal/pkg/kiro/tokens.go backend/internal/pkg/kiro/tokens_test.go
git commit -m "feat(kiro): input/output token 估算"
```

---

### Task 7: 流式转换（Kiro event-stream → Anthropic SSE 事件）

**Files:**
- Create: `backend/internal/pkg/kiro/stream.go`
- Test: `backend/internal/pkg/kiro/stream_test.go`

**Interfaces:**
- Consumes: Task 1 的 `Decoder`/`NewDecoder`；Task 2 的 `ParseEvent`/`Event`/各事件类型；Task 6 的 `EstimateText`
- Produces:
  - `func NewStreamTranslator(model, messageID string, fakeThinking bool) *StreamTranslator`
  - `func (*StreamTranslator) Feed(chunk []byte) ([]apicompat.AnthropicStreamEvent, error)`
  - `func (*StreamTranslator) Finalize() []apicompat.AnthropicStreamEvent`
  - `func (*StreamTranslator) Usage() apicompat.AnthropicUsage`
  - `func (*StreamTranslator) Credits() float64`
  - `func (*StreamTranslator) SawContent() bool`
  - `type UpstreamError struct { Type, Code, Message string }` + `func (*UpstreamError) Error() string`

> **签名变更说明**：构造函数比先前概览多一个 `messageID` 参数。
> Anthropic 的 `message_start` 需要一个消息 ID，由调用方（网关服务）生成，
> 便于与请求日志关联，也让本包保持纯函数、无随机源。

**要点：**

1. **`toolUseEvent.Input` 是 JSON 字符串分片**，必须按 `toolUseId` 分组累积，
   每片直接作为 `input_json_delta` 的 `partial_json` 透出，累积到 `stop:true` 关闭块。
2. **`SawContent()` 供上层做重试决策** —— 设计文档 §7.2 规定：首字节前失败可重试，
   已经吐出内容后失败**不可**重试（会产生重复内容）。
3. **假思考剥离**：开启时，响应开头的 `<thinking>...</thinking>` 剥成 thinking block。
   剥出的块**不带 signature**（Kiro 给不出），因此只在流内产出，绝不写回 history。
4. **output token 靠估算**（Task 6），cache token 用 `meteringEvent` 的**真实值**。

- [ ] **Step 1: 写失败测试**

创建 `backend/internal/pkg/kiro/stream_test.go`：

```go
package kiro

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

// eventFrame 复用 eventstream_test.go 的 buildFrame 构造一个事件帧。
func eventFrame(t *testing.T, eventType, payload string) []byte {
	t.Helper()
	return buildFrame(t, [][2]string{
		{":message-type", "event"},
		{":event-type", eventType},
	}, []byte(payload))
}

// collectTypes 提取事件类型序列，便于断言整体流形状。
func collectTypes(events []apicompat.AnthropicStreamEvent) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Type)
	}
	return out
}

func TestStreamTextOnly(t *testing.T) {
	t.Parallel()

	tr := NewStreamTranslator("claude-sonnet-4.6", "msg_1", false)

	got, err := tr.Feed(eventFrame(t, "assistantResponseEvent", `{"content":"Hel"}`))
	require.NoError(t, err)
	require.Equal(t, []string{"message_start", "content_block_start", "content_block_delta"}, collectTypes(got))
	require.Equal(t, "msg_1", got[0].Message.ID)
	require.Equal(t, "claude-sonnet-4.6", got[0].Message.Model)
	require.Equal(t, "text", got[1].ContentBlock.Type)
	require.Equal(t, "text_delta", got[2].Delta.Type)
	require.Equal(t, "Hel", got[2].Delta.Text)

	got, err = tr.Feed(eventFrame(t, "assistantResponseEvent", `{"content":"lo"}`))
	require.NoError(t, err)
	require.Equal(t, []string{"content_block_delta"}, collectTypes(got))
	require.Equal(t, "lo", got[0].Delta.Text)

	final := tr.Finalize()
	require.Equal(t, []string{"content_block_stop", "message_delta", "message_stop"}, collectTypes(final))
	require.Equal(t, "end_turn", final[1].Delta.StopReason)
	require.True(t, tr.SawContent())
}

func TestStreamNoContentSawContentFalse(t *testing.T) {
	t.Parallel()

	tr := NewStreamTranslator("m", "msg_1", false)
	require.False(t, tr.SawContent(), "首字节前失败可重试，SawContent 必须为 false")
}

// TestStreamToolUseAccumulatesPartialJSON 覆盖 toolUseEvent 的分片语义。
func TestStreamToolUseAccumulatesPartialJSON(t *testing.T) {
	t.Parallel()

	tr := NewStreamTranslator("m", "msg_1", false)

	_, err := tr.Feed(eventFrame(t, "assistantResponseEvent", `{"content":"working"}`))
	require.NoError(t, err)

	got, err := tr.Feed(eventFrame(t, "toolUseEvent",
		`{"name":"Read","toolUseId":"tu_1","input":"{\"pa","stop":false}`))
	require.NoError(t, err)
	// 文本块要先关闭，再开工具块。
	require.Equal(t, []string{"content_block_stop", "content_block_start", "content_block_delta"}, collectTypes(got))
	require.Equal(t, "tool_use", got[1].ContentBlock.Type)
	require.Equal(t, "tu_1", got[1].ContentBlock.ID)
	require.Equal(t, "Read", got[1].ContentBlock.Name)
	require.Equal(t, "input_json_delta", got[2].Delta.Type)
	require.Equal(t, `{"pa`, got[2].Delta.PartialJSON)

	got, err = tr.Feed(eventFrame(t, "toolUseEvent",
		`{"name":"Read","toolUseId":"tu_1","input":"th\":1}","stop":true}`))
	require.NoError(t, err)
	require.Equal(t, []string{"content_block_delta", "content_block_stop"}, collectTypes(got))
	require.Equal(t, `th":1}`, got[0].Delta.PartialJSON)

	final := tr.Finalize()
	require.Equal(t, []string{"message_delta", "message_stop"}, collectTypes(final))
	require.Equal(t, "tool_use", final[0].Delta.StopReason, "有工具调用时 stop_reason 必须是 tool_use")
}

func TestStreamTwoToolCallsGetDistinctIndices(t *testing.T) {
	t.Parallel()

	tr := NewStreamTranslator("m", "msg_1", false)

	got, err := tr.Feed(eventFrame(t, "toolUseEvent", `{"name":"A","toolUseId":"tu_1","input":"{}","stop":true}`))
	require.NoError(t, err)
	firstIdx := *got[1].Index

	got, err = tr.Feed(eventFrame(t, "toolUseEvent", `{"name":"B","toolUseId":"tu_2","input":"{}","stop":true}`))
	require.NoError(t, err)
	secondIdx := *got[0].Index

	require.Equal(t, firstIdx+1, secondIdx, "第二个工具块的 index 必须递增")
}

// TestStreamMetadataStopReason 是 metadataEvent 的端到端回归：
// 漏掉这个事件会让 stop_reason 永远退化为 end_turn。
func TestStreamMetadataStopReason(t *testing.T) {
	t.Parallel()

	tr := NewStreamTranslator("m", "msg_1", false)
	_, err := tr.Feed(eventFrame(t, "assistantResponseEvent", `{"content":"x"}`))
	require.NoError(t, err)

	got, err := tr.Feed(eventFrame(t, "metadataEvent", `{"stopReason":"max_tokens"}`))
	require.NoError(t, err)
	require.Empty(t, got, "metadataEvent 本身不产出 SSE 事件")

	final := tr.Finalize()
	require.Equal(t, "max_tokens", final[1].Delta.StopReason)
}

func TestStreamMeteringFillsUsageAndCredits(t *testing.T) {
	t.Parallel()

	tr := NewStreamTranslator("m", "msg_1", false)
	_, err := tr.Feed(eventFrame(t, "assistantResponseEvent", `{"content":"hello world"}`))
	require.NoError(t, err)

	_, err = tr.Feed(eventFrame(t, "meteringEvent",
		`{"unit":"credit","usage":2.5,"cacheReadInputTokens":100,"cacheCreationInputTokens":20}`))
	require.NoError(t, err)

	usage := tr.Usage()
	require.Equal(t, 100, usage.CacheReadInputTokens, "cache token 必须用上游真实值")
	require.Equal(t, 20, usage.CacheCreationInputTokens)
	require.Positive(t, usage.OutputTokens, "output token 由估算得出")
	require.InDelta(t, 2.5, tr.Credits(), 1e-9)
}

func TestStreamFakeThinkingStripsBlock(t *testing.T) {
	t.Parallel()

	tr := NewStreamTranslator("m", "msg_1", true)

	var all []apicompat.AnthropicStreamEvent
	for _, chunk := range []string{"<thinking>let me ", "reason</thinking>", "final answer"} {
		got, err := tr.Feed(eventFrame(t, "assistantResponseEvent", `{"content":`+quoteJSON(chunk)+`}`))
		require.NoError(t, err)
		all = append(all, got...)
	}
	all = append(all, tr.Finalize()...)

	var thinking, text string
	var sawThinkingBlock, sawTextBlock bool
	for _, e := range all {
		if e.Type == "content_block_start" && e.ContentBlock != nil {
			switch e.ContentBlock.Type {
			case "thinking":
				sawThinkingBlock = true
			case "text":
				sawTextBlock = true
			}
		}
		if e.Type == "content_block_delta" && e.Delta != nil {
			thinking += e.Delta.Thinking
			text += e.Delta.Text
		}
	}

	require.True(t, sawThinkingBlock)
	require.True(t, sawTextBlock)
	require.Equal(t, "let me reason", thinking)
	require.Equal(t, "final answer", text)
}

func TestStreamFakeThinkingWithoutTagIsAllText(t *testing.T) {
	t.Parallel()

	tr := NewStreamTranslator("m", "msg_1", true)

	var all []apicompat.AnthropicStreamEvent
	got, err := tr.Feed(eventFrame(t, "assistantResponseEvent", `{"content":"just a plain answer"}`))
	require.NoError(t, err)
	all = append(all, got...)
	all = append(all, tr.Finalize()...)

	var text string
	for _, e := range all {
		if e.Type == "content_block_delta" && e.Delta != nil {
			require.Empty(t, e.Delta.Thinking)
			text += e.Delta.Text
		}
	}
	require.Equal(t, "just a plain answer", text)
}

func TestStreamExceptionFrameReturnsError(t *testing.T) {
	t.Parallel()

	raw := buildFrame(t, [][2]string{
		{":message-type", "exception"},
		{":exception-type", "ThrottlingException"},
	}, []byte(`{"message":"slow down"}`))

	tr := NewStreamTranslator("m", "msg_1", false)
	_, err := tr.Feed(raw)
	require.Error(t, err)

	var upstream *UpstreamError
	require.ErrorAs(t, err, &upstream)
	require.Equal(t, "ThrottlingException", upstream.Type)
	require.Contains(t, upstream.Message, "slow down")
}

func TestStreamFinalizeIsIdempotent(t *testing.T) {
	t.Parallel()

	tr := NewStreamTranslator("m", "msg_1", false)
	_, err := tr.Feed(eventFrame(t, "assistantResponseEvent", `{"content":"x"}`))
	require.NoError(t, err)

	first := tr.Finalize()
	require.NotEmpty(t, first)
	require.Empty(t, tr.Finalize(), "重复 Finalize 不得重复产出事件")
}
```

在测试文件底部加入辅助函数：

```go
// quoteJSON 把字符串编码为 JSON 字符串字面量（含引号）。
func quoteJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
```

并在 import 块加入 `"encoding/json"`。

- [ ] **Step 2: 运行测试确认失败**

```bash
cd backend && go test ./internal/pkg/kiro/ -run TestStream -v
```

Expected: FAIL —— `undefined: NewStreamTranslator`。

- [ ] **Step 3: 实现 `stream.go`**

```go
package kiro

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

// thinkingOpenTag / thinkingCloseTag 是假思考模式下模型被要求使用的标签。
const (
	thinkingOpenTag  = "<thinking>"
	thinkingCloseTag = "</thinking>"
)

// UpstreamError 是 Kiro 返回的异常帧。
type UpstreamError struct {
	Type    string
	Code    string
	Message string
}

func (e *UpstreamError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("kiro upstream %s (%s): %s", e.Type, e.Code, e.Message)
	}
	return fmt.Sprintf("kiro upstream %s: %s", e.Type, e.Message)
}

// blockKind 标识当前打开的 content block 类型。
type blockKind int

const (
	blockNone blockKind = iota
	blockThinking
	blockText
	blockToolUse
)

// thinkingPhase 是假思考剥离的状态机阶段。
type thinkingPhase int

const (
	// phasePending：还在判断响应是否以 <thinking> 开头。
	phasePending thinkingPhase = iota
	// phaseInThinking：正在思考块内部。
	phaseInThinking
	// phaseText：已确定后续全部是正文。
	phaseText
)

// StreamTranslator 把 Kiro 的 event-stream 增量翻译成 Anthropic SSE 事件。
// 非并发安全，每个上游响应一个实例。
type StreamTranslator struct {
	model        string
	messageID    string
	fakeThinking bool
	dec          *Decoder

	started   bool
	finalized bool

	nextIndex int
	openKind  blockKind
	openIndex int

	phase   thinkingPhase
	gateBuf string

	curToolID string
	toolCount int

	outputText strings.Builder
	stopReason string
	credits    float64
	cacheRead  int
	cacheWrite int
	sawContent bool
}

// NewStreamTranslator 创建翻译器。messageID 由调用方生成，便于与请求日志关联。
// fakeThinking 与请求侧的注入开关必须一致，否则思考标签会漏进正文。
func NewStreamTranslator(model, messageID string, fakeThinking bool) *StreamTranslator {
	t := &StreamTranslator{
		model:        model,
		messageID:    messageID,
		fakeThinking: fakeThinking,
		dec:          NewDecoder(),
		phase:        phasePending,
	}
	if !fakeThinking {
		t.phase = phaseText
	}
	return t
}

// SawContent 返回是否已经向客户端吐出过内容。
// 上层据此决定失败是否可重试：首字节前可重试，已出内容不可重试。
func (t *StreamTranslator) SawContent() bool { return t.sawContent }

// Credits 返回本次请求消耗的 Kiro credits（来自 meteringEvent）。
func (t *StreamTranslator) Credits() float64 { return t.credits }

// Usage 返回计费用量。cache token 是上游真实值；output token 是估算值 ——
// Kiro 不提供 input/output token，这是既定计费口径。
// InputTokens 由调用方用 EstimateRequestInput 填充。
func (t *StreamTranslator) Usage() apicompat.AnthropicUsage {
	return apicompat.AnthropicUsage{
		OutputTokens:             EstimateText(t.outputText.String()),
		CacheReadInputTokens:     t.cacheRead,
		CacheCreationInputTokens: t.cacheWrite,
	}
}

// Feed 消费一段上游字节，返回应当下发给客户端的 Anthropic 事件。
func (t *StreamTranslator) Feed(chunk []byte) ([]apicompat.AnthropicStreamEvent, error) {
	frames, err := t.dec.Feed(chunk)
	if err != nil {
		return nil, err
	}

	var out []apicompat.AnthropicStreamEvent
	for _, f := range frames {
		ev, perr := ParseEvent(f)
		if perr != nil {
			return out, perr
		}

		events, herr := t.handle(ev)
		out = append(out, events...)
		if herr != nil {
			return out, herr
		}
	}
	return out, nil
}

func (t *StreamTranslator) handle(ev Event) ([]apicompat.AnthropicStreamEvent, error) {
	var out []apicompat.AnthropicStreamEvent

	switch ev.Kind {
	case EventAssistantResponse:
		if ev.Assistant == nil || ev.Assistant.Content == "" {
			return nil, nil
		}
		out = append(out, t.ensureStarted()...)
		out = append(out, t.routeContent(ev.Assistant.Content)...)

	case EventToolUse:
		if ev.ToolUse == nil {
			return nil, nil
		}
		out = append(out, t.ensureStarted()...)
		out = append(out, t.handleToolUse(ev.ToolUse)...)

	case EventMetadata:
		if ev.Metadata != nil && ev.Metadata.StopReason != "" {
			t.stopReason = ev.Metadata.StopReason
		}

	case EventMetering:
		if ev.Metering != nil {
			t.credits += ev.Metering.Usage
			t.cacheRead += ev.Metering.CacheReadInputTokens
			t.cacheWrite += ev.Metering.CacheCreationInputTokens
		}

	case EventException:
		ex := ev.Exception
		if ex == nil {
			ex = &Exception{Type: "UnknownException"}
		}
		return out, &UpstreamError{Type: ex.Type, Code: ex.Code, Message: ex.Message}

	case EventContextUsage, EventCodeReference, EventUnknown:
		// 无需下发给客户端。
	}

	return out, nil
}

// ensureStarted 首次产出前发出 message_start。
func (t *StreamTranslator) ensureStarted() []apicompat.AnthropicStreamEvent {
	if t.started {
		return nil
	}
	t.started = true

	return []apicompat.AnthropicStreamEvent{{
		Type: "message_start",
		Message: &apicompat.AnthropicResponse{
			ID:      t.messageID,
			Type:    "message",
			Role:    "assistant",
			Content: []apicompat.AnthropicContentBlock{},
			Model:   t.model,
			Usage:   apicompat.AnthropicUsage{},
		},
	}}
}

// routeContent 把一段文本按假思考状态机分流到 thinking 块或 text 块。
func (t *StreamTranslator) routeContent(s string) []apicompat.AnthropicStreamEvent {
	var out []apicompat.AnthropicStreamEvent

	for s != "" {
		switch t.phase {
		case phasePending:
			t.gateBuf += s
			s = ""

			trimmed := strings.TrimLeft(t.gateBuf, " \t\r\n")
			switch {
			case strings.HasPrefix(trimmed, thinkingOpenTag):
				t.phase = phaseInThinking
				s = strings.TrimPrefix(trimmed, thinkingOpenTag)
				t.gateBuf = ""
			case strings.HasPrefix(thinkingOpenTag, trimmed):
				// 仍可能是 <thinking> 的前缀，继续等待更多字节。
			default:
				t.phase = phaseText
				s = t.gateBuf
				t.gateBuf = ""
			}

		case phaseInThinking:
			idx := strings.Index(s, thinkingCloseTag)
			if idx < 0 {
				out = append(out, t.emitThinking(s)...)
				s = ""
				break
			}
			out = append(out, t.emitThinking(s[:idx])...)
			s = s[idx+len(thinkingCloseTag):]
			t.phase = phaseText

		case phaseText:
			out = append(out, t.emitText(s)...)
			s = ""
		}
	}

	return out
}

func (t *StreamTranslator) emitThinking(s string) []apicompat.AnthropicStreamEvent {
	if s == "" {
		return nil
	}
	t.sawContent = true

	var out []apicompat.AnthropicStreamEvent
	if t.openKind != blockThinking {
		out = append(out, t.closeBlock()...)
		out = append(out, t.openBlockOf(blockThinking, &apicompat.AnthropicContentBlock{Type: "thinking"}))
	}
	out = append(out, apicompat.AnthropicStreamEvent{
		Type:  "content_block_delta",
		Index: intPtr(t.openIndex),
		Delta: &apicompat.AnthropicDelta{Type: "thinking_delta", Thinking: s},
	})
	return out
}

func (t *StreamTranslator) emitText(s string) []apicompat.AnthropicStreamEvent {
	if s == "" {
		return nil
	}
	t.sawContent = true
	t.outputText.WriteString(s)

	var out []apicompat.AnthropicStreamEvent
	if t.openKind != blockText {
		out = append(out, t.closeBlock()...)
		out = append(out, t.openBlockOf(blockText, &apicompat.AnthropicContentBlock{Type: "text"}))
	}
	out = append(out, apicompat.AnthropicStreamEvent{
		Type:  "content_block_delta",
		Index: intPtr(t.openIndex),
		Delta: &apicompat.AnthropicDelta{Type: "text_delta", Text: s},
	})
	return out
}

func (t *StreamTranslator) handleToolUse(tu *ToolUse) []apicompat.AnthropicStreamEvent {
	var out []apicompat.AnthropicStreamEvent

	if tu.ToolUseID != t.curToolID || t.openKind != blockToolUse {
		out = append(out, t.closeBlock()...)
		out = append(out, t.openBlockOf(blockToolUse, &apicompat.AnthropicContentBlock{
			Type:  "tool_use",
			ID:    tu.ToolUseID,
			Name:  tu.Name,
			Input: json.RawMessage("{}"),
		}))
		t.curToolID = tu.ToolUseID
		t.toolCount++
		t.sawContent = true
	}

	if tu.Input != "" {
		out = append(out, apicompat.AnthropicStreamEvent{
			Type:  "content_block_delta",
			Index: intPtr(t.openIndex),
			Delta: &apicompat.AnthropicDelta{Type: "input_json_delta", PartialJSON: tu.Input},
		})
	}

	if tu.Stop {
		out = append(out, t.closeBlock()...)
		t.curToolID = ""
	}

	return out
}

func (t *StreamTranslator) openBlockOf(kind blockKind, block *apicompat.AnthropicContentBlock) apicompat.AnthropicStreamEvent {
	t.openKind = kind
	t.openIndex = t.nextIndex
	t.nextIndex++

	return apicompat.AnthropicStreamEvent{
		Type:         "content_block_start",
		Index:        intPtr(t.openIndex),
		ContentBlock: block,
	}
}

func (t *StreamTranslator) closeBlock() []apicompat.AnthropicStreamEvent {
	if t.openKind == blockNone {
		return nil
	}
	idx := t.openIndex
	t.openKind = blockNone
	return []apicompat.AnthropicStreamEvent{{Type: "content_block_stop", Index: intPtr(idx)}}
}

// Finalize 冲刷缓冲、关闭未完块，并发出收尾事件。重复调用返回空。
func (t *StreamTranslator) Finalize() []apicompat.AnthropicStreamEvent {
	if t.finalized {
		return nil
	}
	t.finalized = true

	var out []apicompat.AnthropicStreamEvent

	// 门控缓冲里可能还压着未判定的内容（响应太短、始终像 <thinking> 的前缀）。
	if t.gateBuf != "" {
		t.phase = phaseText
		buffered := t.gateBuf
		t.gateBuf = ""
		out = append(out, t.ensureStarted()...)
		out = append(out, t.emitText(buffered)...)
	}

	out = append(out, t.closeBlock()...)

	usage := t.Usage()
	out = append(out, apicompat.AnthropicStreamEvent{
		Type: "message_delta",
		Delta: &apicompat.AnthropicDelta{
			StopReason: mapStopReason(t.stopReason, t.toolCount),
		},
		Usage: &usage,
	})
	out = append(out, apicompat.AnthropicStreamEvent{Type: "message_stop"})

	return out
}

// mapStopReason 把 Kiro 的 stopReason 映射为 Anthropic 的 stop_reason。
// 有工具调用时恒为 tool_use —— 否则客户端不会继续工具轮次。
func mapStopReason(reason string, toolCount int) string {
	if toolCount > 0 {
		return "tool_use"
	}

	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "max_tokens", "max_output_tokens", "length":
		return "max_tokens"
	case "model_context_window_exceeded", "context_window_exceeded":
		return "model_context_window_exceeded"
	case "refusal", "content_filter", "content_filtered", "guardrail_intervened":
		return "refusal"
	case "stop_sequence":
		return "stop_sequence"
	case "pause_turn":
		return "pause_turn"
	default:
		return "end_turn"
	}
}

func intPtr(v int) *int { return &v }
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd backend && go test ./internal/pkg/kiro/ -v
```

Expected: 全部 PASS。若 `TestStreamFakeThinkingStripsBlock` 失败，重点检查
`routeContent` 的门控分支 —— `strings.HasPrefix(thinkingOpenTag, trimmed)`
这一支是「当前缓冲还只是开标签的前缀，继续等」的意思，方向不能写反。

- [ ] **Step 5: 跑整个 A 组的完整回归并提交**

```bash
cd backend && gofmt -w internal/pkg/kiro/ && go vet ./internal/pkg/kiro/ && go test ./internal/pkg/kiro/ -count=1
git add backend/internal/pkg/kiro/stream.go backend/internal/pkg/kiro/stream_test.go
git commit -m "feat(kiro): Kiro event-stream 转 Anthropic SSE 事件流"
```

---

> **计划文档状态：** Task 1-7 已完整展开。Task 8-20 见下方接口契约，逐组补齐。

## 后续任务概览（待补齐为完整步骤）

### A 组剩余

| Task | 交付物 | 关键接口 |
|---|---|---|
| 8 | `models.go` + `endpoints.go` + `errors.go` | `func MapModel(string) string`；`func DefaultModels() []string`；`type Endpoint struct{URL, Origin, AmzTarget, Name string}`；`func EndpointsFor(isAPIKey bool, region string) []Endpoint`（见 spec §7.1 四端点表）；`type Signal int` 常量 `SignalOK/SignalAuthExpired/SignalOverage/SignalRateLimited/SignalNetworkRegion/SignalBadRequest/SignalSuspended/SignalCreditsExhausted` + `func Classify(status int, body []byte) Signal` —— **必测 `INVALID_MODEL_ID` → `SignalNetworkRegion`（不标记账号故障）、400 → `SignalBadRequest`（不重试不转移）** |


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
