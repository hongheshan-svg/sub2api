package kiro

import (
	"encoding/json"
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

// TestStreamFakeThinkingOpenTagSplitAcrossFrames 覆盖开标签本身被拆到多个帧的情况——
// 之前的测试只拆了闭标签和正文，开标签总是整段到达，从未真正走到
// routeContent 里"缓冲区还只是 <thinking> 前缀，继续等待更多字节"这一分支。
func TestStreamFakeThinkingOpenTagSplitAcrossFrames(t *testing.T) {
	t.Parallel()

	tr := NewStreamTranslator("m", "msg_1", true)

	var all []apicompat.AnthropicStreamEvent
	for _, chunk := range []string{"<thi", "nking>let me ", "reason</thinking>", "final answer"} {
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
	require.NotContains(t, thinking, "<thinking>")
	require.NotContains(t, thinking, "</thinking>")
	require.NotContains(t, text, "<thinking>")
	require.NotContains(t, text, "</thinking>")
}

// TestStreamFakeThinkingLookAlikeTagIsAllText 覆盖"像标签但不是标签"的情况——
// 开头是 "<think" 但后面接的不是 "ing>"，必须整段落回正文，不能被误判为思考标签。
func TestStreamFakeThinkingLookAlikeTagIsAllText(t *testing.T) {
	t.Parallel()

	tr := NewStreamTranslator("m", "msg_1", true)

	got, err := tr.Feed(eventFrame(t, "assistantResponseEvent", `{"content":"<think about it>"}`))
	require.NoError(t, err)
	final := tr.Finalize()

	var thinking, text string
	var sawThinkingBlock bool
	for _, e := range append(got, final...) {
		if e.Type == "content_block_start" && e.ContentBlock != nil && e.ContentBlock.Type == "thinking" {
			sawThinkingBlock = true
		}
		if e.Type == "content_block_delta" && e.Delta != nil {
			thinking += e.Delta.Thinking
			text += e.Delta.Text
		}
	}

	require.False(t, sawThinkingBlock, "\"<think about it>\" 不是思考标签，不应开出 thinking block")
	require.Empty(t, thinking)
	require.Equal(t, "<think about it>", text)
}

// TestStreamFakeThinkingShortPrefixFlushedOnFinalize 覆盖流在标签判定完成前就结束的情况——
// 内容比 "<thinking>" 本身还短，门控缓冲必须在 Finalize 时冲刷成正文，不能被吞掉。
func TestStreamFakeThinkingShortPrefixFlushedOnFinalize(t *testing.T) {
	t.Parallel()

	tr := NewStreamTranslator("m", "msg_1", true)

	got, err := tr.Feed(eventFrame(t, "assistantResponseEvent", `{"content":"<th"}`))
	require.NoError(t, err)
	// message_start 由 ensureStarted 无条件先发；标签判定未完成前不应有任何内容块事件。
	require.Equal(t, []string{"message_start"}, collectTypes(got))

	final := tr.Finalize()

	var text string
	for _, e := range final {
		if e.Type == "content_block_delta" && e.Delta != nil {
			text += e.Delta.Text
			require.Empty(t, e.Delta.Thinking)
		}
	}
	require.Equal(t, "<th", text, "门控缓冲里的内容必须在 Finalize 时冲刷为正文")
	require.True(t, tr.SawContent(), "冲刷出的内容也算已吐出内容")
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

// quoteJSON 把字符串编码为 JSON 字符串字面量（含引号）。
func quoteJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
