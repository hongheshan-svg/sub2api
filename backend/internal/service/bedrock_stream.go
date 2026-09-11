package service

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// handleBedrockStreamingResponse 处理 Bedrock InvokeModelWithResponseStream 的 EventStream 响应
// Bedrock 返回 AWS EventStream 二进制格式，每个事件的 payload 中 chunk.bytes 是 base64 编码的
// Claude SSE 事件 JSON。本方法解码后转换为标准 SSE 格式写入客户端。
func (s *GatewayService) handleBedrockStreamingResponse(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	account *Account,
	startTime time.Time,
	model string,
) (*streamingResult, error) {
	w := c.Writer
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	if v := resp.Header.Get("x-amzn-requestid"); v != "" {
		c.Header("x-request-id", v)
	}

	usage := &ClaudeUsage{}
	var firstTokenMs *int
	clientDisconnected := false

	// Bedrock EventStream 使用 application/vnd.amazon.eventstream 二进制格式。
	// 每个帧结构：total_length(4) + headers_length(4) + prelude_crc(4) + headers + payload + message_crc(4)
	// 但更实用的方式是使用行扫描找 JSON chunks，因为 Bedrock 的响应在二进制帧中。
	// 我们使用 EventStream decoder 来正确解析。
	decoder := newBedrockEventStreamDecoder(resp.Body)

	type decodeEvent struct {
		payload []byte
		buf     []byte // payload 的底层帧缓冲区，消费者用完 payload 后必须 putBedrockFrameBuf(buf) 归还
		err     error
	}
	events := make(chan decodeEvent, 16)
	done := make(chan struct{})
	sendEvent := func(ev decodeEvent) bool {
		select {
		case events <- ev:
			return true
		case <-done:
			return false
		}
	}
	var lastReadAt atomic.Int64
	lastReadAt.Store(time.Now().UnixNano())

	go func() {
		defer close(events)
		for {
			payload, buf, err := decoder.Decode()
			if err != nil {
				if err == io.EOF {
					return
				}
				_ = sendEvent(decodeEvent{err: err})
				return
			}
			lastReadAt.Store(time.Now().UnixNano())
			if !sendEvent(decodeEvent{payload: payload, buf: buf}) {
				// 消费者已放弃读取（客户端断开/流超时），buf 交给 GC 回收，不放回 pool。
				return
			}
		}
	}()
	defer close(done)

	streamInterval := time.Duration(0)
	if s.cfg != nil && s.cfg.Gateway.StreamDataIntervalTimeout > 0 {
		streamInterval = time.Duration(s.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
	}

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				if !clientDisconnected {
					flusher.Flush()
				}
				return &streamingResult{usage: usage, firstTokenMs: firstTokenMs, clientDisconnect: clientDisconnected}, nil
			}
			if ev.err != nil {
				if clientDisconnected {
					return &streamingResult{usage: usage, firstTokenMs: firstTokenMs, clientDisconnect: true}, nil
				}
				if errors.Is(ev.err, context.Canceled) || errors.Is(ev.err, context.DeadlineExceeded) {
					return &streamingResult{usage: usage, firstTokenMs: firstTokenMs, clientDisconnect: true}, nil
				}
				return &streamingResult{usage: usage, firstTokenMs: firstTokenMs}, fmt.Errorf("bedrock stream read error: %w", ev.err)
			}

			// payload 是 JSON，提取 chunk.bytes（base64 编码的 Claude SSE 事件数据）。
			// extractBedrockChunkData 会把需要的内容拷贝进新分配的 sseData，用完 ev.payload
			// 后立刻归还底层帧缓冲区，不等到本次循环结束。
			sseData := extractBedrockChunkData(ev.payload)
			putBedrockFrameBuf(ev.buf)
			if sseData == nil {
				continue
			}

			if firstTokenMs == nil {
				ms := int(time.Since(startTime).Milliseconds())
				firstTokenMs = &ms
			}

			// 转换 Bedrock 特有的 amazon-bedrock-invocationMetrics 为标准 Anthropic usage 格式
			// 同时移除该字段避免透传给客户端
			sseData = transformBedrockInvocationMetrics(sseData)

			// 解析 SSE 事件数据提取 usage
			parseSSEUsagePassthrough(string(sseData), usage)

			// 确定 SSE event type
			eventType := gjson.GetBytes(sseData, "type").String()

			// 写入标准 SSE 格式
			if !clientDisconnected {
				var writeErr error
				if eventType != "" {
					_, writeErr = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, sseData)
				} else {
					_, writeErr = fmt.Fprintf(w, "data: %s\n\n", sseData)
				}
				if writeErr != nil {
					clientDisconnected = true
					logger.LegacyPrintf("service.gateway", "[Bedrock] Client disconnected during streaming, continue draining for usage: account=%d", account.ID)
				} else {
					flusher.Flush()
				}
			}

		case <-intervalCh:
			lastRead := time.Unix(0, lastReadAt.Load())
			if time.Since(lastRead) < streamInterval {
				continue
			}
			if clientDisconnected {
				return &streamingResult{usage: usage, firstTokenMs: firstTokenMs, clientDisconnect: true}, nil
			}
			logger.LegacyPrintf("service.gateway", "[Bedrock] Stream data interval timeout: account=%d model=%s interval=%s", account.ID, model, streamInterval)
			if s.rateLimitService != nil {
				s.rateLimitService.HandleStreamTimeout(ctx, account, model)
			}
			return &streamingResult{usage: usage, firstTokenMs: firstTokenMs}, fmt.Errorf("stream data interval timeout")
		}
	}
}

// extractBedrockChunkData 从 Bedrock EventStream payload 中提取 Claude SSE 事件数据
// Bedrock payload 格式：{"bytes":"<base64-encoded-json>"}
func extractBedrockChunkData(payload []byte) []byte {
	b64 := gjson.GetBytes(payload, "bytes").String()
	if b64 == "" {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil
	}
	return decoded
}

// transformBedrockInvocationMetrics 将 Bedrock 特有的 amazon-bedrock-invocationMetrics
// 转换为标准 Anthropic usage 格式，并从 SSE 数据中移除该字段。
//
// Bedrock Invoke 返回的 message_delta 事件可能包含：
//
//	{"type":"message_delta","delta":{...},"amazon-bedrock-invocationMetrics":{"inputTokenCount":150,"outputTokenCount":42}}
//
// 转换为：
//
//	{"type":"message_delta","delta":{...},"usage":{"input_tokens":150,"output_tokens":42}}
func transformBedrockInvocationMetrics(data []byte) []byte {
	metrics := gjson.GetBytes(data, "amazon-bedrock-invocationMetrics")
	if !metrics.Exists() || !metrics.IsObject() {
		return data
	}

	// 移除 Bedrock 特有字段
	data, _ = sjson.DeleteBytes(data, "amazon-bedrock-invocationMetrics")

	// 如果已有标准 usage 字段，不覆盖
	if gjson.GetBytes(data, "usage").Exists() {
		return data
	}

	// 转换 camelCase → snake_case 写入 usage
	inputTokens := metrics.Get("inputTokenCount")
	outputTokens := metrics.Get("outputTokenCount")
	if inputTokens.Exists() {
		data, _ = sjson.SetBytes(data, "usage.input_tokens", inputTokens.Int())
	}
	if outputTokens.Exists() {
		data, _ = sjson.SetBytes(data, "usage.output_tokens", outputTokens.Int())
	}

	return data
}

// bedrockEventStreamDecoder 解码 AWS EventStream 二进制帧
// EventStream 帧格式：
//
//	[total_byte_length: 4 bytes]
//	[headers_byte_length: 4 bytes]
//	[prelude_crc: 4 bytes]
//	[headers: variable]
//	[payload: variable]
//	[message_crc: 4 bytes]
type bedrockEventStreamDecoder struct {
	reader *bufio.Reader
}

func newBedrockEventStreamDecoder(r io.Reader) *bedrockEventStreamDecoder {
	return &bedrockEventStreamDecoder{
		reader: bufio.NewReaderSize(r, 64*1024),
	}
}

// bedrockFrameBufPool 复用 EventStream 帧的 payload 缓冲区（headers+payload+message_crc），
// 减少高频流式解码下的堆分配。Decode 把缓冲区的所有权转交调用方，调用方读完 payload 内容
// 后必须调用 putBedrockFrameBuf 归还；归还前绝不能再引用对应的 payload 切片。
var bedrockFrameBufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, 0, 8*1024)
		return &buf
	},
}

func getBedrockFrameBuf(n int) []byte {
	ptr, ok := bedrockFrameBufPool.Get().(*[]byte)
	if !ok || ptr == nil {
		return make([]byte, n)
	}
	buf := *ptr
	if cap(buf) < n {
		return make([]byte, n)
	}
	return buf[:n]
}

// putBedrockFrameBuf 归还一个不再被任何 goroutine 引用的帧缓冲区。
func putBedrockFrameBuf(buf []byte) {
	if buf == nil {
		return
	}
	buf = buf[:0]
	bedrockFrameBufPool.Put(&buf)
}

// Decode 读取下一个 EventStream 帧，返回 chunk 类型事件的 payload。
// 第二个返回值 buf 是 payload 的底层完整缓冲区：调用方用完 payload 后必须调用
// putBedrockFrameBuf(buf) 归还给 sync.Pool，且归还后不能再访问 payload。
func (d *bedrockEventStreamDecoder) Decode() ([]byte, []byte, error) {
	for {
		// 读取 prelude: total_length(4) + headers_length(4) + prelude_crc(4) = 12 bytes
		var preludeArr [12]byte
		prelude := preludeArr[:]
		if _, err := io.ReadFull(d.reader, prelude); err != nil {
			return nil, nil, err
		}

		// 验证 prelude CRC（AWS EventStream 使用标准 CRC32 / IEEE）
		preludeCRC := bedrockReadUint32(prelude[8:12])
		if crc32.Checksum(prelude[0:8], crc32IEEETable) != preludeCRC {
			return nil, nil, fmt.Errorf("eventstream prelude CRC mismatch")
		}

		totalLength := bedrockReadUint32(prelude[0:4])
		headersLength := bedrockReadUint32(prelude[4:8])

		if totalLength < 16 { // minimum: 12 prelude + 4 message_crc
			return nil, nil, fmt.Errorf("invalid eventstream frame: total_length=%d", totalLength)
		}

		// 读取 headers + payload + message_crc
		remaining := int(totalLength) - 12
		if remaining <= 0 {
			continue
		}
		data := getBedrockFrameBuf(remaining)
		if _, err := io.ReadFull(d.reader, data); err != nil {
			putBedrockFrameBuf(data)
			return nil, nil, err
		}

		// 验证 message CRC（覆盖 prelude + headers + payload）
		messageCRC := bedrockReadUint32(data[len(data)-4:])
		h := crc32.New(crc32IEEETable)
		_, _ = h.Write(prelude)
		_, _ = h.Write(data[:len(data)-4])
		if h.Sum32() != messageCRC {
			putBedrockFrameBuf(data)
			return nil, nil, fmt.Errorf("eventstream message CRC mismatch")
		}

		// 解析 headers
		headers := data[:headersLength]
		payload := data[headersLength : len(data)-4] // 去掉 message_crc

		// 从 headers 中提取 :event-type
		eventType := extractEventStreamHeaderValue(headers, ":event-type")

		// 只处理 chunk 事件
		if eventType == "chunk" {
			// payload 是完整的 JSON，包含 bytes 字段；data 的所有权转交调用方，
			// 调用方用完 payload 后必须调用 putBedrockFrameBuf(data) 归还。
			return payload, data, nil
		}

		// 检查异常事件
		exceptionType := extractEventStreamHeaderValue(headers, ":exception-type")
		if exceptionType != "" {
			msg := string(payload)
			putBedrockFrameBuf(data)
			return nil, nil, fmt.Errorf("bedrock exception: %s: %s", exceptionType, msg)
		}

		messageType := extractEventStreamHeaderValue(headers, ":message-type")
		if messageType == "exception" || messageType == "error" {
			msg := string(payload)
			putBedrockFrameBuf(data)
			return nil, nil, fmt.Errorf("bedrock error: %s", msg)
		}

		// 跳过其他事件类型（如 initial-response），归还缓冲区后继续读下一帧
		putBedrockFrameBuf(data)
	}
}

// extractEventStreamHeaderValue 从 EventStream headers 二进制数据中提取指定 header 的字符串值
// EventStream header 格式：
//
//	[name_length: 1 byte][name: variable][value_type: 1 byte][value: variable]
//
// value_type = 7 表示 string 类型，前 2 bytes 为长度
func extractEventStreamHeaderValue(headers []byte, targetName string) string {
	pos := 0
	for pos < len(headers) {
		if pos >= len(headers) {
			break
		}
		nameLen := int(headers[pos])
		pos++
		if pos+nameLen > len(headers) {
			break
		}
		name := string(headers[pos : pos+nameLen])
		pos += nameLen

		if pos >= len(headers) {
			break
		}
		valueType := headers[pos]
		pos++

		switch valueType {
		case 7: // string
			if pos+2 > len(headers) {
				return ""
			}
			valueLen := int(bedrockReadUint16(headers[pos : pos+2]))
			pos += 2
			if pos+valueLen > len(headers) {
				return ""
			}
			value := string(headers[pos : pos+valueLen])
			pos += valueLen
			if name == targetName {
				return value
			}
		case 0: // bool true
			if name == targetName {
				return "true"
			}
		case 1: // bool false
			if name == targetName {
				return "false"
			}
		case 2: // byte
			pos++
			if name == targetName {
				return ""
			}
		case 3: // short
			pos += 2
			if name == targetName {
				return ""
			}
		case 4: // int
			pos += 4
			if name == targetName {
				return ""
			}
		case 5: // long
			pos += 8
			if name == targetName {
				return ""
			}
		case 6: // bytes
			if pos+2 > len(headers) {
				return ""
			}
			valueLen := int(bedrockReadUint16(headers[pos : pos+2]))
			pos += 2 + valueLen
		case 8: // timestamp
			pos += 8
		case 9: // uuid
			pos += 16
		default:
			return "" // 未知类型，无法继续解析
		}
	}
	return ""
}

// crc32IEEETable is the CRC32 / IEEE table used by AWS EventStream.
var crc32IEEETable = crc32.MakeTable(crc32.IEEE)

func bedrockReadUint32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func bedrockReadUint16(b []byte) uint16 {
	return uint16(b[0])<<8 | uint16(b[1])
}
