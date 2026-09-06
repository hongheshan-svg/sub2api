# Kiro 作为第一类平台接入 —— 阶段 1（Anthropic 协议面 MVP）

- **Date:** 2026-09-03
- **Status:** Draft (design)
- **Scope:** 把 Kiro（Amazon Q Developer / AWS CodeWhisperer 后端）接入 sub2api，
  作为独立的 `kiro` 平台，阶段 1 只交付 Anthropic `/v1/messages` 协议面
- **参考实现（已本地通读）:** `TsinHzl/kiro2cc-proxy`(Rust)、`Quorinex/Kiro-Go`(Go)、
  `lorne-luo/kiro-go-proxy`(Go)、`justlovemaki/AIClient2API`(Node)
- **Fork-only:** 本特性只存在于本 fork，永不向 upstream 提交

---

## 1. 背景与问题

Kiro 账号可以访问 Claude 系模型。此前本 fork 通过**外部 kiro2api + `upstream` 透传账号**
使用 Kiro，代价是：账号凭证、token 刷新、额度、调度全部在 sub2api 之外，
出问题无法定位（历史上的 "Kiro400" 事故就是因为看不到请求转换细节）。
代码里目前只剩一个 legacy 常量 `service/domain_constants.go:53` 的 `PlatformKiro`。

**Kiro 上游根本不是 Anthropic 形状**，而是 CodeWhisperer 的 `generateAssistantResponse`：

| 维度 | 实际情况 |
|---|---|
| 端点 | `POST https://q.{region}.amazonaws.com/generateAssistantResponse` |
| 认证 | `Bearer <accessToken>` + `profileArn`，多种刷新端点 |
| 请求体 | `conversationState.{chatTriggerType, conversationId, currentMessage, history[]}` |
| 无 system 角色 | system prompt 只能拼进第一条 user message |
| 强制角色交替 | 必须 user 开头、user/assistant 严格交替 |
| 无原生 thinking | 四份参考实现都是「假思考」：注入 XML 指令 + 出口正则剥离 |
| 响应 | AWS event-stream 二进制帧 |
| 计费 | 只给 credits，**没有 input/output token** |

---

## 2. 目标 / 非目标

**阶段 1 目标**

- `kiro` 成为一等平台，可建分组、可挂账号、可被调度
- 四种凭证接入（Social / Builder ID / IdC SSO / API Key），token 自动刷新
- Claude Code 经 `/v1/messages` 端到端跑通：流式、工具调用、图片
- 额度可见（`getUsageLimits`），credits 耗尽自动冷却
- 流量正常计费落账

**阶段 1 非目标**（各自独立立项）

- 阶段 2：补齐剩余平台 switch 分支（composite 纳入、渠道监控 v1/v2、调度阈值、
  错误透传规则、模型限流、`upstream_models`、`scheduler_snapshot`、账号测试、
  `simple_mode` 默认分组）、端点顺序设置项、假思考开关 UI
- 阶段 3：OpenAI `/v1/chat/completions` 协议面
- 阶段 4：Codex `/responses` 协议面 + `gpt-5.6-sol/terra/luna` 模型

---

## 3. 决策记录

| # | 决策 | 备选与否决理由 |
|---|---|---|
| D1 | **复活 `PlatformKiro` 做第一类平台** | 备选「anthropic 平台 + kiro 账号类型」（Bedrock 先例，`account.go:1263`）触点少得多，但无法独立暴露 Kiro 的 credits 额度语义与 `gpt-5.6-*` 模型 |
| D2 | **最终暴露三个协议面**，阶段 1 只做 Anthropic | 分阶段交付，每阶段可独立上线 |
| D3 | **四种凭证方式全支持** | 见 §5 |
| D4 | **计费口径：credits 管调度，估算 token 管计费** | 对齐仓库现有 Antigravity 模式（credits 只做冷却，计费仍走 token） |
| D5 | **转换层用「Anthropic 作枢纽」** | 见下 |
| D6 | IdC 授权码经 **sub2api 自建回调页** 回流 | 备选「手工粘回调 URL」（Kiro-Go 做法）体验差 |

### D5 详述：为什么 Anthropic 作枢纽

仓库已有成熟的三向协议转换矩阵 `internal/pkg/apicompat`（六个方向齐全，测例密集，
覆盖 reasoning passback / tool pairing / parallel tool / cache creation / stream lifecycle）：

```
AnthropicToChatCompletionsRequest   ChatCompletionsResponseToAnthropic   ChatCompletionsChunkToAnthropicEvents
AnthropicToResponses                AnthropicToResponsesResponse         AnthropicEventToResponsesEvents
ResponsesToAnthropicRequest         ResponsesToAnthropic                 ResponsesEventToAnthropicEvents
```

因此 `pkg/kiro` **只实现 Anthropic ⇄ Kiro 一对转换**。阶段 3/4 不写新转换器，
入站 OpenAI/Responses 请求先经 apicompat 桥成 Anthropic 形状再进 Kiro，
出站 Anthropic 事件流再桥回去。

- 收益：阶段 3/4 从「约 2800 行新转换器」塌缩为「接线 + 平台分支」；
  角色规整 / system 拼接 / schema sanitize 只有一份，不会漂移
- 代价：阶段 4 是两跳转换，reasoning item 可能双重损失 ——
  但 Kiro 本就没有原生 reasoning（是假思考），该损失实际不存在
- 否决「自建统一 IR」：`apicompat` 已事实上扮演 IR，再造一个是重复造轮
- 否决「三套独立直连转换器」：公共逻辑三份必然漂移

`apicompat.AnthropicRequest` / `AnthropicResponse` / `AnthropicStreamEvent` 已承载
Kiro 需要的全部语义：`System`(string 或 blocks)、content block 的
text/thinking/image/tool_use/tool_result、`Tools.InputSchema`、`CacheControl`、
`Usage` 含 cache token。

---

## 4. 架构与包边界

### 4.1 新增 `internal/pkg/kiro/`（纯函数层，无 DB/Redis 依赖）

depguard 只约束 `internal/service/**` 与 `internal/handler/**`，`internal/pkg/**`
不受限，因此这一层可以完全可单测。

| 文件 | 职责 |
|---|---|
| `eventstream.go` | AWS event-stream 帧解码：`[TotalLen u32][HeaderLen u32][PreludeCRC u32][Headers][Payload][MsgCRC u32]`，prelude 固定 12B，上限 16MB，headers 为 AWS 的 10 种类型化值 |
| `events.go` | 事件类型定义与分发（见 §6.2） |
| `request.go` | `BuildRequest(*apicompat.AnthropicRequest, Opts) (*Request, error)` |
| `schema.go` | JSON Schema sanitize |
| `stream.go` | `Feed([]byte) []apicompat.AnthropicStreamEvent` |
| `tokens.go` | token 估算（移植 Kiro-Go `proxy/token_estimator.go`） |
| `models.go` | 模型 ID 映射与清单 |

**帧解码器移植来源**：照 `kiro2cc-proxy/src/kiro/parser/{frame,header,decoder,crc}.rs`
移植（有完整 CRC 校验）。**不要**照 `kiro-go-proxy/parser/parser.go` ——
它靠在缓冲区里搜索 `{"content":` 等字面量切分 JSON，payload 内出现同样字面量时会错切。

### 4.2 新增 service 层（结构对齐 `antigravity_*` 家族）

`kiro_gateway_service.go`、`kiro_token_provider.go`、`kiro_token_refresher.go`、
`kiro_oauth_service.go`、`kiro_quota_fetcher.go`、`kiro_endpoints.go`

### 4.3 接线点（阶段 1 最小集）

| 文件 | 改动 |
|---|---|
| `internal/domain/constants.go` | `PlatformKiro` 提升为一等常量 |
| `internal/service/domain_constants.go:51-53` | 删除 legacy 注释，加入 `AllowedQuotaPlatforms` |
| `migrations/234_kiro_platform.sql` | **必须**：扩 `user_platform_quotas` 与 `composite_model_routes` 的 platform CHECK |
| `internal/service/token_refresh_service.go:139` 后 | `{platform: PlatformKiro, refresher: kiroRefresher, executor: kiroRefresher}` |
| `internal/server/routes/gateway.go:195` | `/v1/messages` 增加 kiro 分支 → `h.KiroGateway.Messages` |
| `internal/service/wire.go` + `cmd/server/wire.go` | provider set，随后 `go generate ./cmd/server` |
| `internal/handler/kiro_gateway_handler.go` | 新增 |
| `internal/handler/admin/kiro_oauth_handler.go` | 新增 |
| 前端 | 账号表单、授权向导、额度展示、分组平台选项 |

`Account.credentials` 是 JSONB `map[string]any`，凭证字段**不需要迁移**。

### 4.4 ⚠️ 必须与 `AllowedQuotaPlatforms` 同 PR 的迁移

迁移 `224_user_platform_quotas_add_cn_providers.sql` 的头注释记录了一次**生产事故**：
平台进了 `AllowedQuotaPlatforms` 但 CHECK 约束没扩 → `BulkInsertInitial` 是单条多行
INSERT，一行违约整条语句中止 → 注册路径 fail-open 吞错 → **新用户拿到零条配额行
= 无限额**。grok 在 `157` 号迁移踩过同一个坑。

因此 `234_kiro_platform.sql` 与 `AllowedQuotaPlatforms` 的改动**必须在同一个 PR**，
且迁移用 `DROP CONSTRAINT IF EXISTS` + 超集约束保证可重入。

### 4.5 wire 注意事项

`wire_gen.go` 的 invoice 块是手工维护的（`go generate` 会在 invoice 的
`NotificationService` 上失败）。改 provider set 后用 `go build` 验证，
不要盲目接受 regen 结果。

---

## 5. 凭证模型与生命周期

### 5.1 `Account.Type` 复用现有值

不新增 `AccountTypeKiro`。Social / Builder ID / IdC 均为 `AccountTypeOAuth`，
API Key 为 `AccountTypeAPIKey`。判别字段是 `credentials["auth_method"]`
（对齐 Kiro-Go 的 `account.AuthMethod`），从而避开一整批 `Account.Type` 校验分支。

### 5.2 credentials JSONB schema

| key | social | builder_id | idc | api_key |
|---|---|---|---|---|
| `auth_method` | `"social"` | `"builder_id"` | `"idc"` | `"api_key"` |
| `refresh_token` / `access_token` / `expires_at` | ✔ | ✔ | ✔ | — |
| `client_id` / `client_secret` | — | ✔ | ✔ | — |
| `issuer_url` | — | `https://view.awsapps.com/start` | 组织自有 start URL | — |
| `region` | ✔ | ✔ | ✔ | ✔ |
| `profile_arn` | ✔ | ✔ | ✔ | 不使用 |
| `api_key` | — | — | — | ✔ |
| `machine_id` | ✔ | ✔ | ✔ | ✔ |
| `social_provider` | google / github | — | — | — |

### 5.3 刷新路径与初始授权

| auth_method | 刷新 | 初始授权 |
|---|---|---|
| `social` | `POST https://prod.{region}.auth.desktop.kiro.dev/refreshToken`，body 仅 `{refreshToken}` | 见下方 ⚠️ |
| `builder_id` | `POST https://oidc.{region}.amazonaws.com/token` + `{clientId, clientSecret, refreshToken, grantType:"refresh_token"}` | **device_code**：`/client/register`(grantTypes 含 `urn:ietf:params:oauth:grant-type:device_code`) → `/device_authorization` → 后台展示 userCode + verificationUri → 轮询 `/token` |
| `idc` | 同 `builder_id` | **authorization_code + PKCE**：`/client/register`(`issuerUrl` = 组织 start URL，scopes = `codewhisperer:{completions,analysis,conversations,transformations,taskassist}`) → `/authorize?...code_challenge_method=S256` → 自建回调页 → `/token` |
| `api_key` | 不刷新 | 直接粘贴 |

**AWS SSO OIDC 不支持 password grant**，`client/register` 只接受
`authorization_code` / `refresh_token` / `device_code`。因此「后台表单填用户名密码
由服务端直接换 token」在 API 层面不存在；用户名/密码始终输在 AWS 自己的门户页面上。
（唯一的替代是内嵌无头浏览器代填门户表单 —— 已否决：镜像 +300MB、必须可解密存明文
密码、MFA/验证码挡死、AWS 改页面即全挂。）

> **⚠️ `social` 的初始授权形态待实现时核实。** AIClient2API 观察到的 social 流是
> `GET https://prod.{region}.auth.desktop.kiro.dev/login?...&redirect_uri=kiro://kiro.kiroAgent/authenticate-success`
> —— redirect_uri 是**桌面端自定义 URI scheme 深链**，不是 http(s) 回调。
> 若该端点不接受任意 http(s) redirect_uri，则**自建回调页对 social 这条线不适用**，
> 需退化为「管理员粘贴 refreshToken」或「粘回调 URL」。
> 这只影响 social；`idc` 与 `builder_id` 走标准 AWS SSO OIDC，不受影响。
> **实现时先用一个真实 Kiro 账号验证该端点是否接受自定义 redirect_uri，再决定 social 的接入形态。**

### 5.4 `kiro_oauth_service.go` 接口形状

照 `GrokOAuthService`（它已有 `GetCapabilities()` 这套按平台门控认证方式的模式）：
`GenerateAuthURL` / `StartDeviceAuth` / `PollDeviceAuth` / `ExchangeCode` /
`RefreshToken` / `RefreshAccountToken` / `ValidateRefreshToken` / `BuildAccountCredentials`。

背景刷新、失败阈值、临时不可调度全部复用 `token_refresh_service` 现有机制。

### 5.5 四个关键设计点

1. **`profile_arn` 每次刷新后必须回写** —— 刷新响应会带 `profileArn`
   （Kiro-Go 的 `RefreshToken` 返回值即为 `(access, refresh, expiresAt, profileArn, err)`）。
   漏写会导致账号运行一段时间后 403。
2. **`machine_id` 一次生成、永久持久化** —— 拼进 `User-Agent` 与 `x-amz-user-agent`
   （`KiroIDE-{ver}-{machineId}`）做设备指纹。每次请求重新生成等于每次都是新设备，
   有触发上游风控的风险。
3. **API Key 账号走不同端点** —— `runtime.{region}.kiro.dev`（AWS JSON 1.0 协议）+
   `tokentype: API_KEY` 请求头 + `Bearer <apiKey>`，且**不带 profileArn**。
4. **授权会话暂存用 `internal/pkg/redissession`，不用进程内存** ——
   Antigravity 与 Grok 用的是进程内存 `SessionStore`；但本设计的 IdC/social 走
   **自建回调页**，多副本部署时回调可能落到另一副本，内存 session 直接丢失。
   （`ent/schema/pending_auth_session.go` 是用户登录流程的表，语义不同，不复用。）
   这是相对现有先例的一处有意改进。

### 5.6 安全边界

密码全程不落库、不进日志。credentials 中最敏感的是 `refresh_token` /
`client_secret` / `api_key`，走现有 credentials 的存储与脱敏路径。

---

## 6. 数据流

### 6.1 出站（Anthropic → Kiro）：`kiro.BuildRequest` 固定流水线

```
1. 模型解析        claude-* → Kiro modelId
2. 工具预处理      description > 10000 字符 → 移入 system prompt；InputSchema 走 sanitize
3. system 拼接     System(string|blocks) → 扁平化文本 → 拼到第一条 user message 之前
4. 消息规整链      无工具时 StripAllToolContent → MergeAdjacent → EnsureFirstIsUser
                   → NormalizeRoles → EnsureAlternating
5. history 构造    除最后一条外 → [{userInputMessage}|{assistantResponseMessage}]
6. currentMessage  最后一条；若为 assistant → 移入 history，content 顶替为 "Continue"
7. 假思考注入      账号级开关，默认关闭
8. images / toolResults → userInputMessageContext
9. 固定字段        origin:"AI_EDITOR"  chatTriggerType:"MANUAL"  conversationId  profileArn
```

### 6.2 入站（Kiro event-stream → Anthropic SSE）

| Kiro 事件 | → Anthropic |
|---|---|
| `assistantResponseEvent{content, modelId}` | `content_block_delta` / `text_delta` |
| `toolUseEvent{name, toolUseId, input, stop}` | `input` 是**流式 JSON 字符串分片**，拼接至 `stop:true`；映射为 `content_block_start(tool_use)` + `input_json_delta` + `content_block_stop` |
| `metadataEvent{stopReason}` | `message_delta.stop_reason` |
| `meteringEvent{usage, cacheReadInputTokens, cacheCreationInputTokens}` | 只喂计费与调度，不进 SSE |
| `contextUsageEvent{contextUsagePercentage}` | 日志 / 监控 |
| `codeReferenceEvent` | 丢弃（开源许可合规追踪） |
| 错误 / 异常帧 | 映射为 Anthropic error 事件 |

`stop_reason` 映射（`mapClaudeStopReason`）：`toolCount > 0` → `tool_use`；
否则 `max_tokens|max_output_tokens|length` → `max_tokens`，
`model_context_window_exceeded|context_window_exceeded` → 同名，
`refusal|content_filter|content_filtered|guardrail_intervened` → `refusal`，
`stop_sequence` / `pause_turn` → 同名，兜底 `end_turn`。

> **⚠️ 事件表必须合并两份参考实现。** `metadataEvent` 只有 Kiro-Go 处理了
> （`proxy/kiro.go:677`，注释写明 "stopReason rides inside metadataEvent on the wire"），
> **kiro2cc-proxy 的 `EventType` 枚举里没有它**。帧解码照 Rust 那份移植（更严谨），
> 但事件表只照一份抄会静默丢掉 `stop_reason`。

### 6.3 有损转换清单

| 丢失项 | 后果 | 对策 |
|---|---|---|
| `system` 拼进 user message | 上游看到的角色改变，指令遵从度可能下降 | 无解（四份实现一致），文档明示 |
| `cache_control` 丢弃 | 无法控制缓存 | Kiro 自有缓存策略（`meteringEvent` 回传真实 cache token），计费用上游真实值 |
| `thinking` 参数无对应 | Claude Code 开启 thinking 时上游不认 | 假思考（注入 XML 指令 + 出口剥离），账号级开关**默认关闭** |
| 剥离出的 thinking block 无 `signature` | 多轮回传时严格客户端可能拒绝 | 只在流内产出，不写进 history 回传 |
| `tool_choice` 丢弃 | 无法强制 / 禁止工具调用 | 无解，文档明示 |
| `temperature` / `top_p` / `stop_sequences` / `max_tokens` 丢弃 | `userInputMessage` 无对应槽位 | 无解；`max_tokens` 影响预扣费，见 §7.4 |
| 强制角色交替 + 合并相邻 | 原始消息边界丢失 | 多个 `tool_result` 同处一条 user message 是合法的，**必须正确聚合进 `userInputMessageContext.toolResults`** —— 最易出 bug，专门测例 |
| assistant prefill → `"Continue"` | prefill 语义丢失，模型重新开始而非续写 | 无解，文档明示 |
| image URL source 无对应 | Kiro 只接受 `{format, source:{bytes}}` | 返回明确错误，**不静默下载**（避免 SSRF 面） |
| `document` / PDF block 无对应 | | 返回明确的不支持错误 |
| input/output token 靠估算 | 账单与上游 credits 不一一对应 | 已定口径（D4）；cache token 用上游真实值 |

### 6.4 conversationId 与粘性会话

Kiro 按 `conversationId` 组织会话。接入现有粘性会话机制：同一 session 复用同一
`conversationId`；**账号切换时必须重新生成** —— 换账号复用旧 id 会被上游拒绝。

### 6.5 JSON Schema sanitize

递归删除空 `required` 数组与所有 `additionalProperties`。
**这是历史上 "Kiro400" 事故的根因**，必须带回归测例。

---

## 7. 错误处理、端点、额度、计费

### 7.1 端点 fallback

| # | 端点 | origin | x-amz-target | 用于 |
|---|---|---|---|---|
| 1 | `q.us-east-1.amazonaws.com/generateAssistantResponse` | `AI_EDITOR` | 无 | OAuth 账号，首选 |
| 2 | `codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse` | `AI_EDITOR` | `AmazonCodeWhispererStreamingService.GenerateAssistantResponse` | fallback |
| 3 | `q.us-east-1.amazonaws.com/generateAssistantResponse` | `AI_EDITOR` | `AmazonQDeveloperStreamingService.SendMessage` | fallback |
| 4 | `runtime.{region}.kiro.dev/`（AWS JSON 1.0） | `KIRO_CLI` | `AmazonCodeWhispererStreamingService.GenerateAssistantResponse` | **仅 API Key 账号** |

端点顺序设置项属阶段 2；阶段 1 按上表硬编码顺序。

### 7.2 错误分类与动作

| 上游信号 | 判定 | 动作 |
|---|---|---|
| 401 / 403 | token 失效 | 强制刷新一次并重试同端点；再失败 → 走现有凭证失败机制（对齐 `grok_credential_failure.go`） |
| 402 | overage 未开启或超上限 | 账号不可调度 + 明确告警（`getUsageLimits.overageConfiguration` 可区分） |
| 429 | **该端点**额度耗尽 | 先换端点；全部端点 429 → 交给现有 `ratelimit_service.go:1129 handle429` |
| `INVALID_MODEL_ID` | **网络/区域问题，不是账号问题** | ⚠️ **绝不标记账号故障、绝不 failover**。大陆直连必现此错，误判会导致首个请求就禁掉整个账号池 |
| 400 / `IMPROPERLY_FORMED_REQUEST` | 本地 schema sanitize 或角色规整有误 | ⚠️ **不重试、不换账号** —— 换账号同样失败，只会烧光整池。记录详细请求摘要 |
| 订阅暂停 / profileArn 不可用 | 账号状态问题 | 禁用账号 + 写明原因（对齐 Kiro-Go `isSuspensionErrorMessage` / `isProfileUnavailableErrorMessage`） |
| credits 耗尽 | 额度耗尽 | 写 `model_rate_limits["KiroCredits"]` 冷却至 `getUsageLimits.nextDateReset` 的真实时间 |
| 流不完整（0 字符 + 无工具 + 无 stopReason） | 上游静默截断 | 对齐 Kiro-Go `classifyStreamIntegrity`：**首字节前**失败可重试，已出字节则不可重试 |

上表两条 ⚠️ 是本次集成最易造成事故之处，各带专门测例。

### 7.3 额度

`kiro_quota_fetcher.go` 实现现有 fetcher 接口形状（`CanFetch` / `FetchQuota` /
`GetProxyURL`，照 `antigravity_quota_fetcher.go`）：

```
GET {q-host}/getUsageLimits?origin=AI_EDITOR&resourceType=AGENTIC_REQUEST
    &isEmailRequired=true&profileArn=...
→ usageBreakdownList[].{currentUsage, usageLimit, nextDateReset, bonuses[], freeTrialInfo}
  subscriptionInfo.subscriptionTitle   (KIRO FREE / KIRO PRO+ ...)
  overageConfiguration.overageStatus   (ENABLED / DISABLED)
```

### 7.4 计费

| 项 | 来源 |
|---|---|
| `CacheCreationTokens` / `CacheReadTokens` | **上游 `meteringEvent` 真实值**，不估算 |
| `InputTokens` / `OutputTokens` | `kiro.EstimateInputTokens` / `EstimateOutputTokens` |
| `billing_mode` | `"token"`（`usage_log.billing_mode` 已有该字段） |

**credits 只记在账号层，不进 `usage_log`。** `usage_log` 无 credits 列，加列需迁移；
而 D4 定的「credits 管调度」只需账号级聚合（quota fetcher 快照 + `meteringEvent` 累加）。
逐请求 credits 对账留待将来需要时再加列。

**预扣费**：`max_tokens` 在转换中被丢弃。实现前需先确认现有预扣费路径是否依赖它；
若依赖，kiro 这条线用「估算 input + 保守 output 上限」兜底。

---

## 8. 测试策略

- `pkg/kiro` 全部表驱动单测，`//go:build unit`
- **帧解码器 golden test**（真实字节序列）：跨 chunk 切分、CRC 校验失败、
  16MB 上限、headers 的 10 种值类型
- **有损转换清单逐条一个测例**，特别是多个 `tool_result` 聚合进
  `userInputMessageContext.toolResults`
- **schema sanitize 带 "Kiro400" 回归测例**
- **错误分类每个信号一个测例**，其中
  `INVALID_MODEL_ID 不标记账号故障`、`400 不 failover` 各一条
- service 层 stub HTTP，验证调度 / 冷却 / 计费落账
- 规模对齐先例：antigravity 16 个测试文件、grok 21 个 → kiro 预期 **15-20 个**
- 提交前跑 `go test -tags=unit ./...` **全模块**
  （窄范围运行会漏掉其他包内的 `//go:build unit` 测试）

---

## 9. 工作量参考

新增平台在本仓库的触点实测（非测试文件）：

| 先例 | 后端 | 前端 |
|---|---|---|
| Antigravity | 45 | 89 |
| Grok | 69 | 93 |
| Kimi | 25 | 48 |

Kiro 的侵入性最接近 Grok（自带 gateway service + OAuth + 额度 fetcher +
token refresher），但更重：额外需要自定义线协议解码器。阶段 1 预计后端
30-40 个文件（其中约 15 个新增），前端 20-30 个。

---

## 10. 开放假设（实现前需确认或可由 review 翻转）

1. **假思考默认关闭** —— 账号级开关。默认开启会往每个请求塞数百 token 的
   XML 指令，且产出的 thinking 是模型自写文本而非真 reasoning。
2. **credits 不入 `usage_log`**（§7.4）—— 若需要逐请求对账，需加列 + 迁移。
3. **预扣费对 `max_tokens` 的依赖**尚未核实（§7.4）。
4. **阶段 1 硬编码端点顺序**，设置项留到阶段 2。
5. `q.{region}` 与 `codewhisperer.{region}` 的区域化行为：Kiro-Go 注释指出
   Amazon Q 是区域化的而 CodeWhisperer 数据面固定，实现时需按账号 region 做
   URL 重写（`regionalizeURL`）。
6. **`social` 是否接受自建 http(s) 回调**（§5.3 ⚠️）—— 需用真实账号验证。
   若不接受，social 退化为粘贴 refreshToken，不影响 `idc` / `builder_id` / `api_key`。

---

## 11. 合规说明

本特性访问的是用户自有 Kiro 账号的配额。参考实现均声明与 AWS / Amazon / Kiro
无从属关系。部署前应自行确认使用方式符合 Kiro 的服务条款。
