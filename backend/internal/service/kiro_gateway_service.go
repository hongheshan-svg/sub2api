package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/gin-gonic/gin"
)

// kiroBillingMode 是 usage_log.billing_mode 的取值。
// kiro 按估算 token 计费；credits 只记在账号层，不逐请求入库（设计文档 §7.4）。
// 目前只被 kiro_billing_test.go（//go:build unit）引用，用于把这个计费口径决定
// 固化成可回归的断言；golangci-lint 默认不带 -tags=unit 运行，看不到那处引用，
// 故显式豁免 unused，而不是假装这个常量在生产代码里已经被消费。
//
//nolint:unused // only referenced from kiro_billing_test.go (//go:build unit)
const kiroBillingMode = "token"

// kiroErrorBodyLimit 限制读取错误响应体的字节数。
const kiroErrorBodyLimit = 64 * 1024

// kiroBadRequestLogBodyLimit 进一步截断写进日志行的响应体片段——
// kiroErrorBodyLimit（64KB）对单条日志行来说仍然太长。
const kiroBadRequestLogBodyLimit = 2 * 1024

// kiroRateLimitedExhaustedCooldown 是三个端点都被限流（SignalRateLimited
// 且 hasMoreEndpoints=false）触发账号转移之后的调度冷却时长。
//
// 比额度耗尽更可能是短期突发限流而非账号级问题，参照 grok 429 类错误的
// grokRateLimitFallbackCooldown（2 分钟）取值量级，但 Kiro 没有 429 响应头
// 可用于精确计算重置时间（不同于 OpenAI 侧 get429FallbackCooldown 那样能读
// x-ratelimit-reset），所以取一个更保守一点的 5 分钟：既比额度耗尽更快重新
// 参与调度，也比几十秒级的瞬时抖动冷却更谨慎，避免在真实限流窗口内被反复
// 选中导致账号池整体抖动。
const kiroRateLimitedExhaustedCooldown = 5 * time.Minute

// kiroSuspendedCooldown 是订阅停用/overage 问题触发失败转移后给账号加的调度
// 冷却时长。跟 credits 耗尽不同，这类问题没有 getUsageLimits 能给出的真实
// reset 时间，用一个保守的固定长窗口——账号需要管理员介入才能恢复（重开
// 订阅/开启 overage），不是几分钟到几小时会自愈的瞬时状态。
const kiroSuspendedCooldown = 4 * time.Hour

// ForwardUpstream 把一次 Anthropic 请求转发到 Kiro 并把响应流式写回客户端。
//
// 失败决策全部委托给 decideKiroAction —— 本函数只负责执行决策。
// 只在同一账号的多个端点间重试；换账号由调用方（Task 18 接线的外层账号
// 选择循环）负责，本函数通过返回 *UpstreamFailoverError 发出信号。
//
// 真实流量必须过本地模型白名单（见 forwardUpstream 的 bypassModelWhitelist
// 参数文档）：不能让一个真实用户的请求因为白名单猜错而白白转发一次
// 消耗真实 Kiro 配额去确认"这个模型到底存不存在"——那是管理员主动发起的
// 测试连接（见 TestConnection）该做的事，不是生产流量该承担的代价。
func (s *KiroGatewayService) ForwardUpstream(ctx context.Context, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
	return s.forwardUpstream(ctx, c, account, body, false)
}

// forwardUpstream 是 ForwardUpstream/TestConnection 共用的核心转发逻辑。
//
// bypassModelWhitelist 为 true 时（仅 TestConnection 使用）：不管
// kiroModelAliases 认不认识这个模型名，都直接转发给 Kiro 真实上游，用
// 真实响应验证它到底支不支持——管理员主动发起的一次性诊断调用，请求体
// 很小（TestConnection 固定用 max_tokens:64 的短提示词），代价可以忽略，
// 换来的是不用靠猜/靠第三方参考实现就能确认一个模型名到底对不对（真实
// 账号测试连续两次证明本地白名单会猜错：claude-fable-5 被误收，
// claude-sonnet-5 被误拒——两次错误方向相反，说明"猜"这件事本身不可靠，
// 能直接问上游的场合就不该猜）。
//
// false 时（真实网关流量）：走 MapModel 的白名单闸门，未收录直接拒绝，
// 不浪费一次真实用户的 Kiro 配额去确认一个大概率是错的模型名——见
// ForwardUpstream 的文档。
func (s *KiroGatewayService) forwardUpstream(ctx context.Context, c *gin.Context, account *Account, body []byte, bypassModelWhitelist bool) (*ForwardResult, error) {
	startTime := time.Now()

	var inbound apicompat.AnthropicRequest
	if err := json.Unmarshal(body, &inbound); err != nil {
		return nil, fmt.Errorf("kiro: decode inbound request: %w", err)
	}

	// 真实账号测试发现：MapModel 之前对任何未识别的模型名（含明显不属于
	// Kiro 的名字）都静默兜底成 claude-sonnet-4.6 并正常转发——客户端会
	// 看到请求"成功"，却从未意识到自己请求的模型从未被真正服务过。现在
	// ok=false 时（真实流量）必须直接拒绝，不能静默换模型再假装成功——
	// 与 Antigravity 的 getMappedModel==""→writeClaudeError 是同一约定
	// （见 MapModel 文档：维护一份准确的白名单，命中就映射、未命中就干净
	// 拒绝）。bypassModelWhitelist 时改用去掉日期后缀的原始请求名直接
	// 转发，不查白名单也不拒绝——见本函数文档。
	upstreamModel, modelOK := kiro.MapModel(inbound.Model)
	if !modelOK {
		if !bypassModelWhitelist {
			return nil, s.writeKiroModelUnsupportedError(c, inbound.Model)
		}
		trimmed := strings.ToLower(strings.TrimSpace(inbound.Model))
		if trimmed == "" {
			return nil, s.writeKiroModelUnsupportedError(c, inbound.Model)
		}
		upstreamModel = trimmed
	}

	endpoints := kiro.EndpointsFor(account.IsKiroAPIKeyAccount(), account.KiroRegion())
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("kiro: no endpoints available for account")
	}

	// conversationId 必须与粘性会话一致，换账号时重新生成（kiro.Options.
	// ConversationID 的两条要求，见 conversationIDFor 的文档）。
	conversationID := s.conversationIDFor(c, account)

	payload, err := kiro.BuildRequest(&inbound, kiro.Options{
		ModelID:        upstreamModel,
		ConversationID: conversationID,
		ProfileArn:     s.profileArnFor(account),
		// Origin 不能留空退化成 BuildRequest 的默认值（AI_EDITOR）——
		// API Key 账号的唯一可用端点要求 origin=KIRO_CLI，用默认值会让每个
		// API Key 账号的请求都带错误的 origin。EndpointsFor 返回的端点组内
		// 所有端点共享同一个 Origin（按账号类型区分，不按具体端点区分），
		// 所以在进入下面的重试循环之前，用 endpoints[0].Origin 就足够。
		Origin:                endpoints[0].Origin,
		FakeThinking:          account.KiroFakeThinking(),
		FakeThinkingMaxTokens: 4000,
		ToolDescMaxLen:        10000,
	})
	if err != nil {
		return nil, fmt.Errorf("kiro: build upstream request: %w", err)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("kiro: encode upstream request: %w", err)
	}

	translator := kiro.NewStreamTranslator(inbound.Model, s.newMessageID(), account.KiroFakeThinking())
	// 必须在 Finalize（streamToClient 内部）之前设置，否则客户端收到的
	// message_delta.usage.input_tokens 会保持默认的 0——Kiro 不提供
	// input token，这一项完全依赖调用方主动写入（StreamTranslator.
	// SetInputTokens 的文档原话）。
	translator.SetInputTokens(kiro.EstimateRequestInput(&inbound))

	// hadMachineID 用于只在"这次请求真的新生成了指纹"时落库一次，
	// 避免同一次转发在端点间重试时反复写 DB。
	hadMachineID := strings.TrimSpace(account.KiroMachineID()) != ""

	var (
		refreshed bool
		lastErr   error
	)

	for i := 0; i < len(endpoints); i++ {
		ep := endpoints[i]
		hasMore := i < len(endpoints)-1

		resp, callErr := s.forwardCallEndpoint(ctx, account, ep, raw)

		// callEndpoint 内部的 EnsureKiroMachineID 会在账号首次使用时就地
		// 生成并写入 account.Credentials，但把"是否新生成"这个信号丢弃了
		// （见 kiro_gateway_upstream.go 的注释）。这里在编排层重新判断：
		// 请求开始前没有指纹、这次调用之后有了，说明是本次新生成的，必须
		// 落库——否则下次请求会再生成一个新的，等于每次都换一台设备。
		if !hadMachineID {
			hadMachineID = true
			s.persistMachineIDIfGenerated(ctx, account)
		}

		if callErr != nil {
			// 传输层失败（DNS/TLS/连接被拒/超时等）：按未知信号处理。callErr
			// 除了赋给 lastErr 外不会再被读取（lastErr 只在循环结束后、且这条
			// 分支必然提前 return 的情况下才会被用到），如果这里不留一条日志，
			// 排查一波连通性故障时只能看到 reason=kiro_unknown, status=0，
			// 完全分不清是 DNS 失败、TLS 失败还是超时。sanitize 后再打印，
			// 对齐 gateway_upstream_transport_error.go 的
			// handleUpstreamTransportError 那套"先 sanitize 再落日志，不要把
			// 原始错误文本直接倒出去"的规矩。
			action := decideKiroAction(kiro.SignalUnknown, translator.SawContent(), refreshed, hasMore)
			var accountID int64
			if account != nil {
				accountID = account.ID
			}
			slog.Warn("kiro_transport_error",
				"account_id", accountID,
				"endpoint", ep.Name,
				"error", sanitizeUpstreamErrorMessage(callErr.Error()),
			)
			lastErr = callErr
			if action == kiroActionNextEndpoint {
				continue
			}
			return nil, s.finishWithAction(ctx, account, action, kiro.SignalUnknown, 0, nil)
		}

		status := resp.StatusCode
		if status < 200 || status >= 300 {
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, kiroErrorBodyLimit))
			_ = resp.Body.Close()

			sig := kiro.Classify(status, errBody)
			action := decideKiroAction(sig, translator.SawContent(), refreshed, hasMore)
			lastErr = fmt.Errorf("kiro: %s returned %d (%s)", ep.Name, status, sig)

			// 留一条足以定位的请求摘要——UpstreamFailoverError.Error() 只吐
			// "upstream error: %d (failover)" 这种不含 body 的固定文案（连
			// "(failover)" 也是写死的，不反映 NextAccountAction 的真实值），
			// 排障时只看日志会完全看不到 Kiro 真实返回了什么。之前只在
			// SignalBadRequest 时才记，其它信号（尤其是被 classifyMarkers
			// 命中、状态码本身对不上号的情况）排障时两眼一抹黑——真实账号
			// 联调就踩过这个坑。所有非 2xx 响应都记一条，不只是 400。
			s.logUpstreamError(account, upstreamModel, sig, status, len(inbound.Tools), errBody)

			switch action {
			case kiroActionRefreshAndRetry:
				if rErr := s.refreshAccountToken(ctx, account); rErr != nil {
					return nil, rErr
				}
				refreshed = true
				i-- // 重试同一端点
				continue
			case kiroActionNextEndpoint:
				continue
			default:
				return nil, s.finishWithAction(ctx, account, action, sig, status, errBody)
			}
		}

		// 成功：把响应写给客户端。resp.Body 由 streamToClient/nonStreamToClient
		// 消费完毕后关闭。Kiro 上游本身只支持流式返回，客户端请求 stream:false
		// 时（I3）在这一层把流式响应收集折叠成一次性 JSON，而不是无视
		// inbound.Stream 一律吐 SSE——这是本网关对外的 Anthropic 兼容协议要求，
		// 不是 Kiro 上游的能力，对齐 Antigravity 的
		// handleClaudeStreamToNonStreaming 解决同一个问题的方式。
		defer func() { _ = resp.Body.Close() }()
		if inbound.Stream {
			return s.streamToClient(ctx, c, account, resp, translator, &inbound, upstreamModel, startTime)
		}
		return s.nonStreamToClient(ctx, c, account, resp, translator, &inbound, upstreamModel, startTime)
	}

	if lastErr == nil {
		lastErr = errors.New("kiro: all endpoints failed")
	}
	return nil, lastErr
}

// streamToClient 边解码上游 event-stream 边把 Anthropic SSE 写给客户端。
//
// ctx / account 是 I1 新增的参数：流内 exception 帧需要经过
// decideKiroAction/finishWithAction 才能触发失败转移/token 刷新/credits
// 冷却/账号禁用，这两者是它们的必需入参。ForwardUpstream 调用本函数时两者
// 都已经在作用域里。
func (s *KiroGatewayService) streamToClient(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	resp *http.Response,
	translator *kiro.StreamTranslator,
	inbound *apicompat.AnthropicRequest,
	upstreamModel string,
	startTime time.Time,
) (*ForwardResult, error) {
	cw := s.newClientWriter(c)

	var firstTokenMs *int
	cw.beforeFirstWrite = func() {
		ms := int(time.Since(startTime).Milliseconds())
		firstTokenMs = &ms
	}

	var streamDisconnect bool

	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			events, feedErr, done := s.feedTranslatorChunk(ctx, account, translator, buf[:n])
			// I2：done/feedErr 由 feedTranslatorChunk 判定，但不管判定结果
			// 是什么，这个 chunk 里已经产出的合法事件必须先写给客户端——
			// 见 feedTranslatorChunk 文档，这里不再重复。
			if len(events) > 0 {
				if !s.writeEvents(cw, events) {
					break // 客户端断开
				}
			}
			if feedErr != nil {
				return nil, feedErr
			}
			if done {
				break
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				if disconnect, handled := handleStreamReadError(readErr, cw.Disconnected(), "kiro upstream"); handled {
					streamDisconnect = disconnect
					break
				}
				if !translator.SawContent() {
					return nil, readErr
				}
			}
			break
		}
	}

	s.writeEvents(cw, translator.Finalize())

	usage := translator.Usage()
	return &ForwardResult{
		Model:            inbound.Model,
		UpstreamModel:    upstreamModel,
		Stream:           inbound.Stream,
		Duration:         time.Since(startTime),
		FirstTokenMs:     firstTokenMs,
		ClientDisconnect: cw.Disconnected() || streamDisconnect,
		Usage: ClaudeUsage{
			// input token 上游不提供，本地估算（设计文档 D4），已通过
			// ForwardUpstream 里的 translator.SetInputTokens 写入。
			InputTokens: usage.InputTokens,
			// output token 同样是估算；cache token 是 meteringEvent 的真实值。
			OutputTokens:             usage.OutputTokens,
			CacheCreationInputTokens: usage.CacheCreationInputTokens,
			CacheReadInputTokens:     usage.CacheReadInputTokens,
		},
	}, nil
}

// feedTranslatorChunk 把一段上游字节喂给 translator，并把结果归约成
// streamToClient/nonStreamToClient 都能直接消费的三元组：
//
//   - events：这个 chunk 产出的合法事件（即便同时有错误，也可能非空——I2）。
//     调用方必须无条件先把它们写/收集下来，再看 err/done。
//   - err：非 nil 时调用方应该把它作为函数最终返回值直接返回（要么是已经
//     经过 decideKiroAction/finishWithAction 分类好的 *UpstreamFailoverError
//     ——I1，要么是一个无法归类为 *kiro.UpstreamError 的其它解码错误）。
//   - done：true 表示调用方的读循环应该停止继续读取上游（不管 err 是否为
//     nil）。err==nil 且 done==true 特指"已经吐出过内容、遇到错误只能优雅
//     截断"这一种情况——调用方应该跳出循环，走到各自的 Finalize/收尾逻辑，
//     而不是把这次调用整体当作失败处理。
//
// 这段逻辑原来完全内联在 streamToClient 里；I3 需要给"客户端请求
// stream:false"再实现一条几乎一样的读循环（Kiro 上游本身只支持流式，
// 非流式响应靠收集全部事件后一次性折叠），把它抽成共享步骤是为了不让这套
// "流内异常帧到底意味着什么"的判断逻辑在两个函数里维护两份、下一次修 bug
// 时只改一边而在另一边留下同样的缺陷。
func (s *KiroGatewayService) feedTranslatorChunk(
	ctx context.Context,
	account *Account,
	translator *kiro.StreamTranslator,
	chunk []byte,
) (events []apicompat.AnthropicStreamEvent, err error, done bool) {
	events, tErr := translator.Feed(chunk)
	if tErr == nil {
		return events, nil, false
	}

	// 上游异常帧：已出内容时只能截断，未出内容时把错误交给
	// decideKiroAction/finishWithAction 分类处理（I1）——之前这里直接把裸
	// 错误原样返回，跳过了失败决策矩阵，流内的鉴权失效/限流/额度耗尽信号
	// 从未触发过失败转移、token 刷新或账号冷却。
	if translator.SawContent() {
		return events, nil, true
	}

	var upstreamErr *kiro.UpstreamError
	if errors.As(tErr, &upstreamErr) {
		sig := kiro.ClassifyUpstreamError(upstreamErr)
		// hasMoreEndpoints 在这里没有意义——已经在某个端点上建立连接成功、
		// 进入流式阶段了，按"没有更多端点可换"处理，对应一次流内终态失败
		// 该有的处理方式。
		action := decideKiroAction(sig, false, false, false)
		if action == kiroActionRefreshAndRetry {
			// streamToClient/nonStreamToClient 都没有自己的重试循环——
			// 它们是 ForwardUpstream 在成功建立一次连接之后调用的单次流式
			// 消费函数，没法从这里发起"刷新 token 后重新发起同一个 HTTP
			// 请求"这种结构性动作。降级为 FailoverAccount：至少保证下一次
			// 请求换一个账号，而不是把一个这里执行不了的动作直接返回给
			// 上层。
			action = kiroActionFailoverAccount
		}
		return events, s.finishWithAction(ctx, account, action, sig, 0, nil), true
	}
	return events, tErr, true
}

// writeKiroModelUnsupportedError 直接把"模型不受支持"写成 Anthropic 协议
// 形状的错误响应给客户端，并标记响应已提交（MarkResponseCommitted）—— 与
// AntigravityGatewayService.writeClaudeError 对 getMappedModel=="" 的处理
// 是同一约定：403 permission_error，调用方 gateway_handler.go 看到
// IsResponseCommitted 为真就不会再尝试写第二份响应。空模型名与不在白名单
// 里的模型名共用这一条路径，用同一种"权限/支持范围"语义描述都合适——
// 前者是"没有指定要哪个模型"，后者是"指定的模型不在这个账号能服务的
// 范围内"，客户端视角下都是"这次请求要不到你想要的模型"。
func (s *KiroGatewayService) writeKiroModelUnsupportedError(c *gin.Context, requestedModel string) error {
	MarkResponseCommitted(c)
	message := fmt.Sprintf("model %q is not supported by this account's platform", requestedModel)
	c.JSON(http.StatusForbidden, map[string]any{
		"type":  "error",
		"error": map[string]any{"type": "permission_error", "message": message},
	})
	return fmt.Errorf("kiro: %s", message)
}

// newClientWriter 设置 SSE 响应头，返回复用的客户端写出器。
//
// antigravityClientWriter 尽管带 antigravity 前缀，实际上是一个不含任何
// Antigravity 专属逻辑的通用 SSE 写出器（断连检测 + 首字节计时钩子），
// 与 handleStreamReadError 一样直接复用，不重复造一份 Kiro 专属版本。
func (s *KiroGatewayService) newClientWriter(c *gin.Context) *antigravityClientWriter {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	flusher, _ := c.Writer.(http.Flusher)
	return newAntigravityClientWriter(c.Writer, flusher, "kiro upstream")
}

// writeEvents 把 Anthropic 流事件序列化成 SSE 帧写给客户端。
// 返回值与 antigravityClientWriter 的写入约定一致：false 表示客户端已断开，
// 调用方应停止继续写入。
func (s *KiroGatewayService) writeEvents(cw *antigravityClientWriter, events []apicompat.AnthropicStreamEvent) bool {
	for _, ev := range events {
		data, err := json.Marshal(ev)
		if err != nil {
			// 不应发生——AnthropicStreamEvent 的所有字段都可序列化。
			// 单个事件失败不应打断整条流，跳过继续写下一个。
			slog.Error("kiro_stream_event_marshal_failed", "event_type", ev.Type, "error", err)
			continue
		}
		if !cw.Fprintf("event: %s\ndata: %s\n\n", ev.Type, data) {
			return false
		}
	}
	return true
}

// logUpstreamError 记录一次被 Kiro 上游拒绝的调用（任意非 2xx 状态码），
// 用于排障定位——UpstreamFailoverError.Error() 只吐不含 body 的固定文案，
// 不看这条日志就完全不知道 Kiro 真实说了什么。只含账号 ID、模型、分类出的
// signal、状态码、工具数与截断后的响应体片段——不含任何凭证。
func (s *KiroGatewayService) logUpstreamError(account *Account, model string, sig kiro.Signal, status int, toolCount int, body []byte) {
	var accountID int64
	if account != nil {
		accountID = account.ID
	}
	snippet := body
	if len(snippet) > kiroBadRequestLogBodyLimit {
		snippet = snippet[:kiroBadRequestLogBodyLimit]
	}
	slog.Warn("kiro_upstream_error",
		"account_id", accountID,
		"model", model,
		"signal", sig.String(),
		"status", status,
		"tool_count", toolCount,
		"response_body", string(snippet),
	)
}

// persistMachineIDIfGenerated 把 callEndpoint 内部首次生成的设备指纹落库。
//
// 必须经 persistAccountCredentials 这个"凭据写入的唯一汇聚点"
// （account_credentials_persistence.go），不能直接 s.accountRepo.Update
// 整行覆写：persistAccountCredentials 带 IsCredentialShadow 早退（防御性
// 措施，避免把凭据误写进凭据透传母账号的影子行——外审第6轮 P1），并在
// repo 支持窄写接口时优先走 UpdateCredentials 而不是整行 Update。这是本
// 仓库除 admin/CRS 同步工具外，唯一一处在真实请求热路径上直接做凭据相关
// 写库的调用点，必须和其它写入路径（token 刷新/订阅补全等）共用同一条
// 安全收敛路径。
func (s *KiroGatewayService) persistMachineIDIfGenerated(ctx context.Context, account *Account) {
	if s == nil || s.accountRepo == nil || account == nil {
		return
	}
	if strings.TrimSpace(account.KiroMachineID()) == "" {
		return
	}
	if err := persistAccountCredentials(ctx, s.accountRepo, account, account.Credentials); err != nil {
		slog.Warn("kiro_machine_id_persist_failed", "account_id", account.ID, "error", err)
	}
}

// refreshAccountToken 刷新账号的 Kiro 令牌并把结果写回调用方持有的 account 指针。
//
// 复用 OAuthRefreshAPI.RefreshIfNeeded（Task 12/13 已经解决分布式锁、DB
// 重读、凭据合并），不要重新发明。refreshWindow 显式给 time.Hour 而不是
// 取巧的极小值——RefreshIfNeeded 内部会先查 executor.NeedsRefresh，窗口
// 太小时，即便这次刷新是被一次真实的 401 触发的，也可能因为本地记录的
// expires_at 还没到而被误判"不需要刷新"从而空转；给足窗口能确保这条
// 反应式刷新路径必然触发一次真实的 executor.Refresh。
func (s *KiroGatewayService) refreshAccountToken(ctx context.Context, account *Account) error {
	if s == nil || s.oauthRefreshAPI == nil {
		return fmt.Errorf("kiro: oauth refresh api is not configured")
	}
	executor := NewKiroTokenRefresher(s.kiroOAuthService)
	result, err := s.oauthRefreshAPI.RefreshIfNeeded(ctx, account, executor, time.Hour)
	if err != nil {
		return fmt.Errorf("kiro: refresh token: %w", err)
	}
	if result != nil && result.Account != nil {
		*account = *result.Account
	}
	return nil
}

// finishWithAction 执行 decideKiroAction 的终态决策（Proceed/RefreshAndRetry/
// NextEndpoint 由调用方在循环里直接处理，不会走到这里）：把失败包装成外层
// 编排能理解的错误，Abort 显式挡住失败转移，Suspended 额外做真实账号禁用。
//
// statusCode/body 由调用方从 ForwardUpstream 循环里已经解析过的上游响应
// 直接传入，而不是本函数再从某个 error 里重新提取——那份状态码和响应体
// 就是 kiro.Classify 用来算出 sig 的原始输入，调用方手里已经有了。
//
// ctx 是 Task 20 新增的参数：SignalCreditsExhausted 分支需要用它发起一次
// 独立超时的 getUsageLimits 现场查询（creditsExhaustedCooldownUntil），
// 并把结果落库（accountRepo.SetModelRateLimit）——两者都要求 context。
func (s *KiroGatewayService) finishWithAction(ctx context.Context, account *Account, action kiroAction, sig kiro.Signal, statusCode int, body []byte) error {
	failover := &UpstreamFailoverError{
		StatusCode:   statusCode,
		ResponseBody: body,
		Reason:       GatewayFailureReason("kiro_" + sig.String()),
	}

	switch action {
	case kiroActionAbort:
		// 不变式：Abort 必须显式挡住失败转移。NextAccountAction 的零值
		// （NextAccountLegacyRetry）代表"允许换账号重试"，留空会让外层编排
		// 照样 failover，等于让 decideKiroAction 的两条红线（NetworkRegion
		// 端点耗尽 / BadRequest）形同虚设。
		failover.NextAccountAction = NextAccountStop

		if sig == kiro.SignalSuspended && s.runtimeBlocker != nil {
			// Abort 只挡住"这一次请求"的失败转移，不影响账号池后续调度；
			// Suspended 是账号自身状态问题，理应额外做真实禁用，否则下一个
			// 请求还会继续被路由过来重复触发同样的失败。
			//
			// 用零值 time.Time{} 表示尽可能长的禁用。
			//
			// 注意：AccountRuntimeBlocker 目前在 wire.go 里唯一的绑定实现是
			// OpenAIGatewayService.BlockAccountScheduling，它对 platform 做了
			// openai/grok 专属门禁（isOpenAIAccount），对 kiro 账号是彻底的
			// no-op；即便是 openai/grok 账号，零值 until 也只会被转成几分钟
			// 量级的过渡冷却（blockAccountSchedulingLocked 里的
			// openAIStopSchedulingBridgeCooldown），不是真正的无限期——真正的
			// 永久禁用在 openai_access_state / upstream_disable 那两处走的是
			// 另一条独立调用链（rateLimitService.handleAuthError /
			// HandleUpstreamError），Kiro 这里没有等价物。这里调用
			// BlockAccountScheduling 是"按接口正确调用"，但只有 Task 18 给
			// KiroGatewayService 接一个真正对 kiro 账号生效的 runtimeBlocker
			// 实现之后，Suspended 账号才会被真实禁用——接线前这行调用本身就
			// 是 no-op。控制端已经在 SDD ledger 里把这条显式带进 Task 18 的
			// 预检要求，不是本任务遗漏。
			s.runtimeBlocker.BlockAccountScheduling(account, time.Time{}, "kiro_suspended")
		}

	case kiroActionFailoverAccount:
		// NextAccountAction 留零值：允许失败转移到下一个账号。
		switch sig {
		case kiro.SignalCreditsExhausted:
			// until 优先来自一次现场 getUsageLimits 查询给出的真实
			// nextDateReset，比 Antigravity 的固定 5 小时冷却更准确
			// （本任务的核心改进，见 creditsExhaustedCooldownUntil 的文档）。
			until := s.creditsExhaustedCooldownUntil(ctx, account)
			if s.runtimeBlocker != nil {
				s.runtimeBlocker.BlockAccountScheduling(account, until, "kiro_credits_exhausted")
			}
			if s.accountRepo != nil {
				if err := s.accountRepo.SetModelRateLimit(ctx, account.ID, kiroCreditsExhaustedKey, until); err != nil {
					slog.Warn("kiro_credits_exhausted_persist_failed", "account_id", account.ID, "error", err)
				}
				s.updateKiroModelRateLimitInCache(ctx, account, kiroCreditsExhaustedKey, until)
			}
		case kiro.SignalSuspended, kiro.SignalOverage:
			// 订阅停用/overage 没有 getUsageLimits 能给出的真实 reset 时间
			// （不像 credits 耗尽），用保守的固定长冷却——这类账号需要管理员
			// 介入才能恢复，不是几分钟到几小时会自愈的瞬时状态（C3）。
			until := time.Now().Add(kiroSuspendedCooldown)
			if s.runtimeBlocker != nil {
				s.runtimeBlocker.BlockAccountScheduling(account, time.Time{}, "kiro_"+sig.String())
				// AccountRuntimeBlocker 目前对 kiro 账号仍是 no-op（见上面
				// kiroActionAbort 分支里 SignalSuspended 那段注释，已在
				// SDD ledger 里登记、留给未来任务接线）——这里保留调用是为了
				// 将来真正给 Kiro 接上 runtimeBlocker 实现之后直接生效，
				// 不需要再补一处调用点。
			}
			if s.accountRepo != nil {
				// 复用 kiroCreditsExhaustedKey 而不是新开一个 key：C4 让
				// modelRateLimitKeysForRequest 对 Kiro 账号无条件检查这一个
				// key，Suspended/Overage/CreditsExhausted 三种原因因此都靠
				// 同一个机制正确排除账号，不需要调度器再多认一个 key。
				if err := s.accountRepo.SetModelRateLimit(ctx, account.ID, kiroCreditsExhaustedKey, until); err != nil {
					slog.Warn("kiro_subscription_issue_persist_failed", "account_id", account.ID, "signal", sig.String(), "error", err)
				}
				s.updateKiroModelRateLimitInCache(ctx, account, kiroCreditsExhaustedKey, until)
			}
		case kiro.SignalRateLimited:
			// 只有端点耗尽（hasMoreEndpoints=false）才会走到这里；还有
			// 端点可换时 decideKiroAction 返回的是 NextEndpoint，不经过
			// finishWithAction。
			until := time.Now().Add(kiroRateLimitedExhaustedCooldown)
			if s.runtimeBlocker != nil {
				s.runtimeBlocker.BlockAccountScheduling(account, until, "kiro_rate_limited")
			}
			if s.accountRepo != nil {
				// 修复前这里只调用了目前对 kiro 恒为 no-op 的 runtimeBlocker，
				// 从未写 model_rate_limits——即便按接口"正确"调用，实际调度
				// 效果是零。补齐与 CreditsExhausted/Suspended/Overage 一致的
				// 落库路径，让端点耗尽也能真正排除账号（C3 顺带修复）。
				if err := s.accountRepo.SetModelRateLimit(ctx, account.ID, kiroCreditsExhaustedKey, until); err != nil {
					slog.Warn("kiro_rate_limited_persist_failed", "account_id", account.ID, "error", err)
				}
				s.updateKiroModelRateLimitInCache(ctx, account, kiroCreditsExhaustedKey, until)
			}
		}
	}

	return failover
}

// creditsExhaustedCooldownUntil 尝试用一次现场 getUsageLimits 查询算出真实的
// 冷却截止时间（相对 Antigravity 固定窗口的核心改进）。这是在一个已经失败
// 的请求路径上做的"顺手"查询——用独立的短超时（5 秒，不是常规额度查询的
// 20 秒），查询失败/超时就直接退回保守冷却，不让客户端因为这次额外探测被
// 拖慢太多，也不把已经很差的失败请求体验搞得更差。
//
// Fix Round 1（Task 20 review Important 发现）：credits 真耗尽时往往是一批
// 并发请求同时失败，原实现让每个失败请求都独立发起一次现场查询，在账号已
// 经出问题的时刻对上游 getUsageLimits 造成惊群。两层防护，缺一不可：
//
//  1. 短路层——先看账号本地副本上 kiroCreditsExhaustedKey 的限流记录：如果
//     还没过期，说明另一个并发请求刚做过这次查询并写回了结果，直接复用，
//     不再打上游。account.Extra 是调用方在同一次请求处理过程中持有的账号
//     快照；SetModelRateLimit 落库后紧跟着的 updateKiroModelRateLimitInCache
//     会把这次查询结果同步写回 account.Extra，所以这一层能在真正命中
//     singleflight 之前就拦掉后续在（大致）同一账号快照上重复调用的请求。
//  2. singleflight 层——短路层拦不住的真正同一时刻的并发请求（各自持有
//     还没被上一次结果更新过的账号快照），用 creditsQuotaFlight 按账号 ID
//     去重，让它们共享同一次现场查询而不是各打一次。
func (s *KiroGatewayService) creditsExhaustedCooldownUntil(ctx context.Context, account *Account) time.Time {
	now := time.Now()
	fallback := now.Add(kiroCreditsFallbackCooldown)

	if account == nil {
		return fallback
	}

	// 层 1：短路——账号本地副本上已有一条还没过期的记录。
	if resetAt := account.modelRateLimitResetAt(kiroCreditsExhaustedKey); resetAt != nil && resetAt.After(now) {
		return *resetAt
	}

	// 层 2：singleflight——真正同一时刻并发的失败请求共享同一次现场查询。
	flightKey := fmt.Sprintf("kiro-credits:%d", account.ID)
	v, err, _ := s.creditsQuotaFlight.Do(flightKey, func() (any, error) {
		shortCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		fetcher := s.creditsQuotaFetcher()
		proxyURL := s.resolveProxyURL(shortCtx, account)
		limits, _, fetchErr := fetcher.fetchUsageLimits(shortCtx, account, proxyURL)
		if fetchErr != nil || limits == nil {
			// 现场查询失败/超时——退回保守冷却，不让 singleflight.Group.Do
			// 自身的 err 承担这条已经有安全兜底值的路径；Do 的 err 只留给
			// "类型断言之外还出了别的问题"这类不该发生的情况。
			return fallback, nil
		}

		b := limits.AgenticRequest()
		until, ok := kiroCreditsCooldownUntil(b, now)
		if !ok {
			// kiroCreditsCooldownUntil 对 b == nil 或未耗尽都返回 ok=false——
			// 都不能当成"不冷却"处理：我们已经确认账号触发了
			// SignalCreditsExhausted（上游 403/429 已经这么分类过一次），这次
			// 现场查询只是拿不到可信的重置时间，必须退回保守冷却，而不是
			// 放弃冷却让账号立刻被重新调度。
			return fallback, nil
		}
		return until, nil
	})
	if err != nil {
		return fallback
	}
	until, ok := v.(time.Time)
	if !ok {
		return fallback
	}
	return until
}

// updateKiroModelRateLimitInCache 立即更新 Redis 中账号的模型限流状态。
//
// 与 AntigravityGatewayService.updateAccountModelRateLimitInCache
// （antigravity_gateway_retry.go）同构但不共享实现——本仓库里每个网关服务
// 各自维护一份，见该方法的文档：这是既有约定，不是需要抽公共 helper 的重复。
func (s *KiroGatewayService) updateKiroModelRateLimitInCache(ctx context.Context, account *Account, modelKey string, resetAt time.Time) {
	if s == nil || s.schedulerSnapshot == nil || account == nil || modelKey == "" {
		return
	}

	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}

	limits, _ := account.Extra["model_rate_limits"].(map[string]any)
	if limits == nil {
		limits = make(map[string]any)
		account.Extra["model_rate_limits"] = limits
	}

	limits[modelKey] = map[string]any{
		"rate_limited_at":     time.Now().UTC().Format(time.RFC3339),
		"rate_limit_reset_at": resetAt.UTC().Format(time.RFC3339),
	}

	if err := s.schedulerSnapshot.UpdateAccountInCache(ctx, account); err != nil {
		slog.Warn("kiro_model_rate_limit_cache_update_failed", "account_id", account.ID, "model_key", modelKey, "error", err)
	}
}

// profileArnFor 返回请求要携带的 profileArn。
// API Key 账号没有 profileArn 语义——EndpointsFor 给它们选的唯一端点
// （Kiro CLI runtime）也不需要，必须留空，否则上游会拒绝请求。
func (s *KiroGatewayService) profileArnFor(account *Account) string {
	if account == nil || account.IsKiroAPIKeyAccount() {
		return ""
	}
	return account.KiroProfileArn()
}

// newMessageID 生成一个 Anthropic 风格的消息 ID，便于把请求日志与响应关联。
func (s *KiroGatewayService) newMessageID() string {
	return "msg_" + randomHex(16)
}

// conversationIDFor 派生 Kiro 的会话标识：同一客户端会话内稳定，换账号必须
// 变化（kiro.Options.ConversationID 的两条要求）。
//
// gateway_scheduling.go 的 sessionHash/stickyAccountID 是账号选择层的粘性
// 机制（选哪个账号），不是本函数要解决的问题——进入 ForwardUpstream 时账号
// 已经选定。ExtractClientSessionID 是协议无关、专门给"同一客户端会话关联"
// 场景用的信号；它的文档明确说明不影响粘性路由/账号选择/上游 prompt
// caching 这些"有意更宽"的会话语义解析——这里只是把它的值单向导入 Kiro
// 请求体，从不回流进调度，不违反那份"不要让调用方污染其它系统"的约定。
// 拿不到显式信号时退化为随机 ID——没有稳定输入就没有值得追求的稳定性。
func (s *KiroGatewayService) conversationIDFor(c *gin.Context, account *Account) string {
	sessionID := ExtractClientSessionID(c)
	if sessionID == "" {
		return "conv_" + randomHex(16)
	}

	var accountID int64
	if account != nil {
		accountID = account.ID
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", sessionID, accountID)))
	return "conv_" + hex.EncodeToString(h[:16])
}

// KiroTestConnectionResult 是管理端"测试连接"功能的结果——与
// AntigravityGatewayService.TestConnectionResult 保持相同的字段形状，
// 方便 AccountTestService 用同一套事件推送逻辑处理两个平台。
type KiroTestConnectionResult struct {
	Text string
}

// TestConnection 测试 Kiro 账号连接：发一个最小的非流式请求，完整复用
// forwardUpstream 的端点选择/失败决策/token 刷新/计费逻辑，与真实请求走的
// 是同一条代码路径——不是另起一套简化的连通性探测，因此这里验证的就是
// 真实转发链路是否工作，而不是仅仅"能不能连上 AWS"。唯一的差异是
// bypassModelWhitelist=true（见 forwardUpstream 文档）：管理员主动点一次
// 测试连接，代价是一个 max_tokens:64 的短请求，完全负担得起直接问 Kiro
// 真实上游"这个模型到底支不支持"，不需要先过本地白名单——真实账号测试
// 已经两次证明本地白名单会猜错（一次误收 claude-fable-5、一次误拒
// claude-sonnet-5，方向相反），能直接问上游拿真实答案的场合就不该猜。
//
// 需要一个 *gin.Context 给 forwardUpstream 写非流式 JSON 响应——这里用
// httptest 合成一个：不是在写测试，是 forwardUpstream 的签名深度依赖
// gin.Context（conversationIDFor 用它读客户端会话信号、nonStreamToClient
// 用它写响应头/JSON body），比另外拆一条不依赖 gin.Context 的平行路径要
// simpler、且保证测试连接与真实流量共享完全相同的行为。
func (s *KiroGatewayService) TestConnection(ctx context.Context, account *Account, modelID string) (*KiroTestConnectionResult, error) {
	testModelID := strings.TrimSpace(modelID)
	if testModelID == "" {
		models := kiro.DefaultModels()
		if len(models) > 0 {
			testModelID = models[0]
		}
	}

	body, err := json.Marshal(map[string]any{
		"model":      testModelID,
		"max_tokens": 64,
		"stream":     false,
		"messages": []map[string]any{
			{"role": "user", "content": "Say 'OK' and nothing else."},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("kiro: build test request: %w", err)
	}

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	// bypassModelWhitelist=true：管理员主动发起的一次性诊断调用，不查本地
	// 白名单——直接问 Kiro 真实上游这个模型到底支不支持（见 forwardUpstream
	// 的文档：真实账号测试连续两次证明本地白名单会猜错，方向还不一样）。
	if _, err := s.forwardUpstream(ctx, ginCtx, account, body, true); err != nil {
		return nil, err
	}

	var resp apicompat.AnthropicResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("kiro: parse test response: %w", err)
	}

	var text strings.Builder
	for _, block := range resp.Content {
		if block.Type == "text" {
			_, _ = text.WriteString(block.Text)
		}
	}
	return &KiroTestConnectionResult{Text: text.String()}, nil
}
