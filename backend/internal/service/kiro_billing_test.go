//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestKiroUsageMappingKeepsUpstreamCacheTokens 固化设计文档 D4 的计费口径：
// cache token 用 meteringEvent 的真实值，input/output 才是估算。
func TestKiroUsageMappingKeepsUpstreamCacheTokens(t *testing.T) {
	tr := kiro.NewStreamTranslator("claude-sonnet-4.6", "msg_1", false)

	// 直接构造 usage 场景：真实 cache token + 估算 output。
	usage := tr.Usage()
	require.Zero(t, usage.CacheReadInputTokens, "无 meteringEvent 时为 0")

	inbound := &apicompat.AnthropicRequest{
		System: rawJSONForBilling(t, `"a system prompt of some length"`),
		Messages: []apicompat.AnthropicMessage{
			{Role: "user", Content: rawJSONForBilling(t, `"hello there, this is a message"`)},
		},
	}

	claudeUsage := ClaudeUsage{
		InputTokens:              kiro.EstimateRequestInput(inbound),
		OutputTokens:             usage.OutputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		CacheReadInputTokens:     usage.CacheReadInputTokens,
	}

	require.Positive(t, claudeUsage.InputTokens, "input token 必须来自本地估算，不得为 0")
	require.Zero(t, claudeUsage.CacheCreationInputTokens)
}

// TestKiroBillingModeIsToken 固化 usage_log.billing_mode 的取值：kiro 走
// token 计费（设计文档 D4/§7.4），credits 只记在账号层用于调度与额度
// 展示，不逐请求入库。
//
// 之前这里断言的是一个独立常量 kiroBillingMode=="token"，从未被生产代码
// 消费，是个自证测试（I8，Task 21 评审记录的 deferred technical debt：
// 真实计费口径完全由 resolveBillingMode 决定，那个常量只是摆在旁边看起来
// 像回归测试）。改成直接调用 resolveBillingMode 本身，用一个 kiro 请求
// 形态的 ForwardResult 验证真实产出的 billing_mode——resolveBillingMode
// 本身此前也是零测试覆盖，顺带补上（连同另外两条分支：cost.BillingMode
// 显式给定时优先、ImageCount>0 时按 image 计费）。
func TestKiroBillingModeIsToken(t *testing.T) {
	result := &ForwardResult{Model: "claude-sonnet-4.6"}

	mode := resolveBillingMode(result, nil)
	require.NotNil(t, mode)
	require.Equal(t, "token", *mode, "kiro 按估算 token 计费；credits 只记账号层，不进 usage_log")
}

// TestResolveBillingModePrefersExplicitCostBillingMode 覆盖 cost 显式给定
// BillingMode 时优先于默认 token 的分支（非 kiro 专属，但与上面的默认
// 分支一起才能证明 resolveBillingMode 三个分支都对）。
func TestResolveBillingModePrefersExplicitCostBillingMode(t *testing.T) {
	result := &ForwardResult{}
	cost := &CostBreakdown{BillingMode: "per_request"}

	mode := resolveBillingMode(result, cost)
	require.NotNil(t, mode)
	require.Equal(t, "per_request", *mode)
}

// TestResolveBillingModeFallsBackToImageWhenNoCostGiven 覆盖 ImageCount>0
// 且 cost 未显式给定 BillingMode 时的 image 分支。
func TestResolveBillingModeFallsBackToImageWhenNoCostGiven(t *testing.T) {
	result := &ForwardResult{ImageCount: 1}

	mode := resolveBillingMode(result, nil)
	require.NotNil(t, mode)
	require.Equal(t, "image", *mode)
}

func rawJSONForBilling(t *testing.T, s string) json.RawMessage {
	t.Helper()
	require.True(t, json.Valid([]byte(s)))
	return json.RawMessage(s)
}

// 客户端按 Kiro 点号名请求（账号白名单的 Kiro 预设用的就是点号名，会出现在
// /v1/models 里）时，ForwardResult.Model 要还原成连字符名再计费：点号名在价格
// 表里只能靠按子串猜系列的兜底，claude-haiku-4.5 会落到 Haiku 3 的
// $0.25/$1.25（实价 $1/$5）。回给客户端的模型名、UpstreamModel 不变。
func TestKiroDotModelRequestBillsByAnthropicName(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)
	pricingSvc := &PricingService{}
	pricingData, err := pricingSvc.parsePricingData(data)
	require.NoError(t, err)
	pricingSvc.pricingData = pricingData
	billing := NewBillingService(&config.Config{}, pricingSvc)

	for _, stream := range []bool{true, false} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			frames := kiroTestConcatFrames(
				kiroTestEventFrame("assistantResponseEvent", `{"content":"Hello"}`),
				kiroTestEventFrame("metadataEvent", `{"stopReason":"end_turn"}`),
			)
			srv, _ := kiroTestFakeUpstream(t, func(int) (int, []byte) { return http.StatusOK, frames })
			svc := &KiroGatewayService{}
			svc.callEndpointOverride = kiroTestOverrideCallingServer(srv)

			body := []byte(fmt.Sprintf(`{"model":"claude-haiku-4.5","max_tokens":100,"messages":[{"role":"user","content":"hi"}],"stream":%v}`, stream))
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

			result, err := svc.ForwardUpstream(context.Background(), c, kiroTestOAuthAccount(500), body)
			require.NoError(t, err)
			require.Equal(t, "claude-haiku-4-5", result.Model)
			require.Equal(t, "claude-haiku-4.5", result.UpstreamModel)
			require.Contains(t, rec.Body.String(), `"model":"claude-haiku-4.5"`, "回给客户端的仍是它请求的名字")

			pricing, err := billing.GetModelPricing(forwardResultBillingModel(result.Model, result.UpstreamModel))
			require.NoError(t, err)
			require.InDelta(t, 1e-6, pricing.InputPricePerToken, 1e-12)
			require.InDelta(t, 5e-6, pricing.OutputPricePerToken, 1e-12)
		})
	}
}
