package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/gin-gonic/gin"
)

// kiroErrorBodyLimit 限制读取错误响应体的字节数。
const kiroErrorBodyLimit = 64 * 1024

// kiroBadRequestLogBodyLimit 进一步截断写进日志行的响应体片段——
// kiroErrorBodyLimit（64KB）对单条日志行来说仍然太长。
const kiroBadRequestLogBodyLimit = 2 * 1024

// kiroCreditsExhaustedCooldown 是账号额度耗尽（SignalCreditsExhausted）
// 触发账号转移之后，给该账号加的调度冷却时长。
//
// 额度耗尽通常要等到下一个计费周期重置或人工充值才会恢复，不是几秒到几分钟
// 内会自愈的瞬时状态。参照本仓库里同属"账号级订阅/额度问题"的先例——
// grok 侧 payment required 类错误取 30 分钟冷却
// （见 account_test_service.go）、openai_images_responses.go 的
// openAIImagesOAuthUnavailableDefaultCooldown 同为 30 分钟——取同一量级。
// 真实账号联调后可再调整。
const kiroCreditsExhaustedCooldown = 30 * time.Minute

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

// ForwardUpstream 把一次 Anthropic 请求转发到 Kiro 并把响应流式写回客户端。
//
// 失败决策全部委托给 decideKiroAction —— 本函数只负责执行决策。
// 只在同一账号的多个端点间重试；换账号由调用方（Task 18 接线的外层账号
// 选择循环）负责，本函数通过返回 *UpstreamFailoverError 发出信号。
func (s *KiroGatewayService) ForwardUpstream(ctx context.Context, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
	startTime := time.Now()

	var inbound apicompat.AnthropicRequest
	if err := json.Unmarshal(body, &inbound); err != nil {
		return nil, fmt.Errorf("kiro: decode inbound request: %w", err)
	}

	upstreamModel := kiro.MapModel(inbound.Model)

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
			// 传输层失败：按未知信号处理。
			action := decideKiroAction(kiro.SignalUnknown, translator.SawContent(), refreshed, hasMore)
			lastErr = callErr
			if action == kiroActionNextEndpoint {
				continue
			}
			return nil, s.finishWithAction(account, action, kiro.SignalUnknown, 0, nil)
		}

		status := resp.StatusCode
		if status < 200 || status >= 300 {
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, kiroErrorBodyLimit))
			_ = resp.Body.Close()

			sig := kiro.Classify(status, errBody)
			action := decideKiroAction(sig, translator.SawContent(), refreshed, hasMore)
			lastErr = fmt.Errorf("kiro: %s returned %d (%s)", ep.Name, status, sig)

			// 400 必须留下足以定位的请求摘要——它是我们自己的构造错误。
			if sig == kiro.SignalBadRequest {
				s.logBadRequest(account, upstreamModel, len(inbound.Tools), errBody)
			}

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
				return nil, s.finishWithAction(account, action, sig, status, errBody)
			}
		}

		// 成功：流式写出。resp.Body 由 streamToClient 消费完毕后关闭。
		defer func() { _ = resp.Body.Close() }()
		return s.streamToClient(c, resp, translator, &inbound, upstreamModel, startTime)
	}

	if lastErr == nil {
		lastErr = errors.New("kiro: all endpoints failed")
	}
	return nil, lastErr
}

// streamToClient 边解码上游 event-stream 边把 Anthropic SSE 写给客户端。
func (s *KiroGatewayService) streamToClient(
	c *gin.Context,
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
			events, tErr := translator.Feed(buf[:n])
			if tErr != nil {
				// 上游异常帧：已出内容时只能截断，未出内容时可返回错误。
				if translator.SawContent() {
					break
				}
				return nil, tErr
			}
			if !s.writeEvents(cw, events) {
				break // 客户端断开
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

// logBadRequest 记录一次因请求本身不合法被上游拒绝的调用，用于排障定位。
// 只含账号 ID、模型、工具数与截断后的响应体片段——不含任何凭证。
func (s *KiroGatewayService) logBadRequest(account *Account, model string, toolCount int, body []byte) {
	var accountID int64
	if account != nil {
		accountID = account.ID
	}
	snippet := body
	if len(snippet) > kiroBadRequestLogBodyLimit {
		snippet = snippet[:kiroBadRequestLogBodyLimit]
	}
	slog.Warn("kiro_bad_request",
		"account_id", accountID,
		"model", model,
		"tool_count", toolCount,
		"response_body", string(snippet),
	)
}

// persistMachineIDIfGenerated 把 callEndpoint 内部首次生成的设备指纹落库。
func (s *KiroGatewayService) persistMachineIDIfGenerated(ctx context.Context, account *Account) {
	if s == nil || s.accountRepo == nil || account == nil {
		return
	}
	if strings.TrimSpace(account.KiroMachineID()) == "" {
		return
	}
	if err := s.accountRepo.Update(ctx, account); err != nil {
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
func (s *KiroGatewayService) finishWithAction(account *Account, action kiroAction, sig kiro.Signal, statusCode int, body []byte) error {
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
			// Suspended 是账号自身状态问题，必须额外做真实禁用，否则下一个
			// 请求还会继续被路由过来重复触发同样的失败。
			// 用零值 time.Time{} 表示无限期禁用，与
			// openai_account_runtime_block_fastpath.go 里"openai_access_state"
			// / "upstream_disable" 两处永久性禁用调用约定一致（对照同文件
			// 229 行附近 429 场景传入具体 cooldownUntil 的用法）。
			s.runtimeBlocker.BlockAccountScheduling(account, time.Time{}, "kiro_suspended")
		}

	case kiroActionFailoverAccount:
		// NextAccountAction 留零值：允许失败转移到下一个账号。
		if s.runtimeBlocker != nil {
			switch sig {
			case kiro.SignalCreditsExhausted:
				s.runtimeBlocker.BlockAccountScheduling(account, time.Now().Add(kiroCreditsExhaustedCooldown), "kiro_credits_exhausted")
			case kiro.SignalRateLimited:
				// 只有端点耗尽（hasMoreEndpoints=false）才会走到这里；还有
				// 端点可换时 decideKiroAction 返回的是 NextEndpoint，不经过
				// finishWithAction。
				s.runtimeBlocker.BlockAccountScheduling(account, time.Now().Add(kiroRateLimitedExhaustedCooldown), "kiro_rate_limited")
			}
		}
	}

	return failover
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
