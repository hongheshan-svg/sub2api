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

	outputText  strings.Builder
	stopReason  string
	credits     float64
	cacheRead   int
	cacheWrite  int
	inputTokens int
	sawContent  bool
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

// SetInputTokens 填充本次请求的 input token 估算值（调用方通常用
// EstimateRequestInput 算出）。必须在 Finalize 之前调用，否则
// message_delta 里的 usage.input_tokens 会保持默认的 0——Kiro 本身不提供
// input token，这一项完全依赖调用方主动写入。
func (t *StreamTranslator) SetInputTokens(n int) { t.inputTokens = n }

// Usage 返回计费用量。cache token 是上游真实值；output token 是估算值 ——
// Kiro 不提供 input/output token，这是既定计费口径。
// output token 统计的是正文文本、假思考剥离出的 thinking 文本、以及工具调用的
// input JSON 三者累加：Anthropic 原生的 usage.output_tokens 本身就包含
// thinking token 和 tool_use.input，本网关对外呈现 Anthropic 兼容接口，口径要
// 与之一致；而且这三者都确实作为对应的 content block 发给了客户端，并非未展示
// 给用户的隐藏消耗。
// InputTokens 由调用方通过 SetInputTokens 填充（通常用 EstimateRequestInput
// 算出），不在这里估算——StreamTranslator 只看到上游流式响应，看不到原始
// 请求内容，没有能力自行估算 input token。
func (t *StreamTranslator) Usage() apicompat.AnthropicUsage {
	return apicompat.AnthropicUsage{
		InputTokens:              t.inputTokens,
		OutputTokens:             EstimateText(t.outputText.String()),
		CacheReadInputTokens:     t.cacheRead,
		CacheCreationInputTokens: t.cacheWrite,
	}
}

// Feed 消费一段上游字节，返回应当下发给客户端的 Anthropic 事件。
// Finalize 之后再调用 Feed 是非法用法，直接返回 (nil, nil)——
// 否则会在已经发出的 message_stop 之后又吐出 content_block 事件，破坏协议顺序。
func (t *StreamTranslator) Feed(chunk []byte) ([]apicompat.AnthropicStreamEvent, error) {
	if t.finalized {
		return nil, nil
	}

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
	t.outputText.WriteString(s)

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
		// 工具调用的输入 JSON 片段也要计入 output token——Anthropic 原生
		// usage.output_tokens 本身包含 tool_use.input，本网关对外呈现
		// Anthropic 兼容接口，口径要与之一致（I2）。
		t.outputText.WriteString(tu.Input)
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

	// message_start 必须无条件先发——哪怕上游整段响应被静默截断、一个字节的
	// 正文都没有（gateBuf 也是空的，从未触发过 handle 里任何一条 ensureStarted
	// 分支），客户端也必须收到结构完整的 message_start -> message_delta ->
	// message_stop，不能只有后两者。ensureStarted 内部有 started 标记，
	// 重复调用是安全的空操作。
	out = append(out, t.ensureStarted()...)

	// 门控缓冲里可能还压着未判定的内容（响应太短、始终像 <thinking> 的前缀）。
	if t.gateBuf != "" {
		t.phase = phaseText
		buffered := t.gateBuf
		t.gateBuf = ""
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
