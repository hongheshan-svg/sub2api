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
//
//nolint:unused // Task 17 把 callEndpoint 接入编排层后，这条链路才会被生产代码触达；本任务只由 unit 测试覆盖。
const kiroUpstreamTimeout = 10 * time.Minute

// KiroGatewayService 负责把 Anthropic 请求转发到 Kiro 上游。
//
// 结构对齐 AntigravityGatewayService：本文件只管「怎么把一次请求发出去」，
// 编排、流式写出与错误分类在 kiro_gateway_service.go。
type KiroGatewayService struct {
	// 依赖在 Task 17 补齐（账号仓储、限流、计费等）。
	// 本文件的 callEndpoint 只依赖 account 与 httpclient，便于独立测试。

	// clientProfile 可被测试或配置覆盖。
	//
	//nolint:unused // 同上：Task 17 接线前只有 unit 测试通过 profile() 读取这个字段。
	clientProfile *kiro.ClientProfile
}

// profile 返回生效的客户端版本组合。
//
//nolint:unused // 只被本文件内 callEndpoint 调用；callEndpoint 本身要到 Task 17 才接入编排层。
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
//
//nolint:unused // Task 16 只交付这个可独立测试的调用单元；Task 17 把它接入编排层（重试/流式/计费）后即被生产代码引用。
func (s *KiroGatewayService) callEndpoint(ctx context.Context, account *Account, ep kiro.Endpoint, payload []byte) (*http.Response, error) {
	if account == nil {
		return nil, fmt.Errorf("kiro: account is required")
	}

	// 首次使用时固化设备指纹。返回 true 说明是新生成的，
	// 调用方（Task 17 的编排层）需要把 credentials 落库。
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
//
//nolint:unused // 只被本文件内 callEndpoint 调用，理由同上。
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

// resolveProxyURL 返回账号预加载的代理地址；仓储兜底留给 Task 17 接线时补上
// （本任务的 KiroGatewayService 还没有 proxyRepo 依赖）。
//
//nolint:unused // 只被本文件内 httpClientFor 调用，理由同上。
func (s *KiroGatewayService) resolveProxyURL(_ context.Context, account *Account) string {
	if account == nil || account.Proxy == nil {
		return ""
	}
	return account.Proxy.URL()
}
