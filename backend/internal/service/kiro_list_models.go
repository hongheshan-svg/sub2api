package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

// ListAvailableModels 调用 AWS 官方 ListAvailableModels，拉取账号真实可用的
// 模型清单——供管理端"同步/刷新模型列表"（AccountTestService.
// SyncUpstreamModelCatalog）使用，取代此前只能靠人工离线调研、手动核对再
// 改 kiroModelAliases 静态表的方式（那次校对本身是真实账号验证过的，
// 但从未接入运行时，见 pkg/kiro/models.go 顶部注释与 commit f6283cc81）。
//
// API Key 账号不使用 profileArn（KiroGatewayService.profileArnFor 的既有
// 约定），这个操作在 API Key 形态下是否可用未经验证，直接拒绝而不是发一个
// 大概率失败、徒然消耗上游配额的请求。
//
// ⚠️ 请求体/响应体的具体字段名是类推而非抓包确认，详见
// pkg/kiro/list_models.go 的说明；这不是它拆成两步的理由——刷新 token
// 这一步本身是完全确定、已被 ForwardUpstream 反复验证过的既有机制，
// 拆开只是为了让单测能绕开需要完整 OAuthRefreshAPI 依赖链的刷新步骤，
// 单独覆盖请求构造/响应解析逻辑。
func (s *KiroGatewayService) ListAvailableModels(ctx context.Context, account *Account) ([]kiro.ModelInfo, error) {
	if s == nil || account == nil {
		return nil, fmt.Errorf("kiro: account is required")
	}
	if account.IsKiroAPIKeyAccount() {
		return nil, fmt.Errorf("kiro: ListAvailableModels is not supported for API Key accounts")
	}

	if err := s.refreshAccountToken(ctx, account); err != nil {
		return nil, fmt.Errorf("kiro: refresh token before listing models: %w", err)
	}

	return s.listAvailableModels(ctx, account)
}

// listAvailableModels 是不含 token 刷新步骤的纯请求/解析逻辑。
func (s *KiroGatewayService) listAvailableModels(ctx context.Context, account *Account) ([]kiro.ModelInfo, error) {
	profileArn := account.KiroProfileArn()
	if profileArn == "" {
		return nil, fmt.Errorf("kiro: account has no profile_arn; ListAvailableModels requires one")
	}
	accessToken := account.KiroAccessToken()
	if accessToken == "" {
		return nil, fmt.Errorf("kiro: account has no access token")
	}

	body, err := kiro.BuildListModelsRequestBody(profileArn)
	if err != nil {
		return nil, fmt.Errorf("kiro: build list models request: %w", err)
	}

	// EnsureKiroMachineID 在账号首次调用时就地生成并写入 account.Credentials；
	// 与 ForwardUpstream 的既有约定一致——新生成的指纹必须落库，否则下次
	// 调用会再生成一个新的，等同于每次都换一台设备（见 finishWithAction
	// 同名逻辑的注释）。这里直接拿到 EnsureKiroMachineID 自己的 generated
	// 返回值判断，不需要像 ForwardUpstream 那样在编排层用前后快照重新推断
	// （callEndpoint 内部调用时把这个信号丢弃了，这里没有这层间接）。
	machineID, generated := EnsureKiroMachineID(account.Credentials)
	if generated {
		s.persistMachineIDIfGenerated(ctx, account)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.listModelsURL(account.KiroRegion()), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("kiro: build list models http request: %w", err)
	}
	req.Header = kiro.BuildHeaders(kiro.HeaderOptions{
		Endpoint:    kiro.Endpoint{Origin: "AI_EDITOR", AmzTarget: kiro.ListModelsAmzTarget},
		BearerToken: accessToken,
		MachineID:   machineID,
		Profile:     s.profile(),
	})

	hc, err := s.httpClientFor(ctx, account)
	if err != nil {
		return nil, err
	}

	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kiro: call ListAvailableModels: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, kiroErrorBodyLimit))
	if err != nil {
		return nil, fmt.Errorf("kiro: read ListAvailableModels response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kiro: ListAvailableModels returned %d: %s", resp.StatusCode, string(respBody))
	}

	models, err := kiro.ParseListModelsResponse(respBody)
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("kiro: ListAvailableModels returned no models")
	}
	return models, nil
}
