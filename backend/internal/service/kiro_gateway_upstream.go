package service

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

// kiroUpstreamTimeout 是单次上游请求的总超时。
// 流式响应可能长时间保持，取值需覆盖最长的一次生成。
const kiroUpstreamTimeout = 10 * time.Minute

// KiroGatewayService 负责把 Anthropic 请求转发到 Kiro 上游。
//
// 结构对齐 AntigravityGatewayService：本文件只管「怎么把一次请求发出去」，
// 编排、流式写出与错误分类在 kiro_gateway_service.go。
//
// 账号选择/并发控制/计费落账不在本结构体的职责内——复用既有的 Messages
// 编排入口（Task 18 接线），这里只持有转发一次请求所需的最小依赖集：
// 账号仓储（重读账号 / 落库设备指纹）、OAuth 刷新入口、Kiro 刷新执行器、
// 运行时调度熔断器。不加限流/计费/配额依赖——那些是 Task 19-21 的范围。
type KiroGatewayService struct {
	// clientProfile 可被测试或配置覆盖。
	clientProfile *kiro.ClientProfile

	accountRepo      AccountRepository
	oauthRefreshAPI  *OAuthRefreshAPI
	kiroOAuthService *KiroOAuthService
	runtimeBlocker   AccountRuntimeBlocker

	// callEndpointOverride 仅供测试使用，生产路径必须为 nil。
	//
	// kiro.EndpointsFor 返回的是真实的 AWS/CLI 域名（q.<region>.amazonaws.com
	// 等），单元测试环境里既连不通也不该连——ForwardUpstream 的集成测试要
	// 验证的是编排逻辑（跨端点重试、decideKiroAction 的执行、流式转译），
	// 不是 Task 16 已经用 httptest 独立测过的请求头构造。测试通过设置这个
	// 字段，把请求路由到本地 httptest 假上游，同时保留 kiro.EndpointsFor
	// 真实的端点数量/顺序，让 hasMoreEndpoints 相关的决策分支照常被真实
	// 触达。见 forwardCallEndpoint。
	callEndpointOverride func(ctx context.Context, account *Account, ep kiro.Endpoint, payload []byte) (*http.Response, error)
}

// forwardCallEndpoint 是 ForwardUpstream 实际使用的调用入口：测试设置了
// callEndpointOverride 时用它，否则用真实的 callEndpoint。
func (s *KiroGatewayService) forwardCallEndpoint(ctx context.Context, account *Account, ep kiro.Endpoint, payload []byte) (*http.Response, error) {
	if s.callEndpointOverride != nil {
		return s.callEndpointOverride(ctx, account, ep, payload)
	}
	return s.callEndpoint(ctx, account, ep, payload)
}

// NewKiroGatewayService 创建转发编排服务。
func NewKiroGatewayService(
	accountRepo AccountRepository,
	oauthRefreshAPI *OAuthRefreshAPI,
	kiroOAuthService *KiroOAuthService,
	runtimeBlocker AccountRuntimeBlocker,
) *KiroGatewayService {
	return &KiroGatewayService{
		accountRepo:      accountRepo,
		oauthRefreshAPI:  oauthRefreshAPI,
		kiroOAuthService: kiroOAuthService,
		runtimeBlocker:   runtimeBlocker,
	}
}

// profile 返回生效的客户端版本组合。
func (s *KiroGatewayService) profile() kiro.ClientProfile {
	if s != nil && s.clientProfile != nil {
		return *s.clientProfile
	}
	return kiro.DefaultClientProfile()
}

// callEndpoint 向指定端点发起一次请求。
//
// 调用方负责按 kiro.EndpointsFor 的顺序重试；本函数只发一次。
// 返回的 *http.Response 由调用方关闭。
func (s *KiroGatewayService) callEndpoint(ctx context.Context, account *Account, ep kiro.Endpoint, payload []byte) (*http.Response, error) {
	if account == nil {
		return nil, fmt.Errorf("kiro: account is required")
	}

	// 首次使用时固化设备指纹。返回 true 说明是新生成的，
	// 调用方（ForwardUpstream）负责把 credentials 落库——见该函数里的
	// persistMachineIDIfGenerated。
	machineID, _ := EnsureKiroMachineID(account.Credentials)

	header := kiro.BuildHeaders(kiro.HeaderOptions{
		Endpoint:    ep,
		BearerToken: account.KiroBearerToken(),
		MachineID:   machineID,
		IsAPIKey:    account.IsKiroAPIKeyAccount(),
		Profile:     s.profile(),
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.URL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("kiro: build request: %w", err)
	}
	req.Header = header

	hc, err := s.httpClientFor(ctx, account)
	if err != nil {
		return nil, err
	}

	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kiro: call %s: %w", ep.Name, err)
	}
	return resp, nil
}

// httpClientFor 返回按账号代理配置构建的客户端。
func (s *KiroGatewayService) httpClientFor(ctx context.Context, account *Account) (*http.Client, error) {
	proxyURL := s.resolveProxyURL(ctx, account)
	hc, err := httpclient.GetClient(httpclient.Options{
		ProxyURL: proxyURL,
		Timeout:  kiroUpstreamTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("kiro: build http client: %w", err)
	}
	return hc, nil
}

// resolveProxyURL 返回账号预加载的代理地址；仓储兜底留给后续任务补上
// （KiroGatewayService 目前还没有 proxyRepo 依赖）。
func (s *KiroGatewayService) resolveProxyURL(_ context.Context, account *Account) string {
	if account == nil || account.Proxy == nil {
		return ""
	}
	return account.Proxy.URL()
}
