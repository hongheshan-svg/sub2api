package kiro

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ListModelsAmzTarget 是 ListAvailableModels 操作的 x-amz-target 头值，
// 供 BuildHeaders(HeaderOptions{Endpoint: Endpoint{AmzTarget: ListModelsAmzTarget}})
// 使用。
const ListModelsAmzTarget = "AmazonCodeWhispererService.ListAvailableModels"

// ListModelsHostFor 返回 ListAvailableModels 该打去哪个 host。
//
// management 是独立于 generateAssistantResponse 用的 q/codewhisperer、
// ListAvailableProfiles 用的 codewhisperer/q 之外的第三个 AWS host 族，
// 按账号 region 拼接——2026-09-06 用真实账号 access_token 直接调用验证过
// 这个 URL 形态本身（见 models.go 顶部注释、commit f6283cc81），但那次验证
// 是手动完成的，从未落进代码库或提交记录，这里是第一次把它接入运行时。
func ListModelsHostFor(region string) string {
	region = strings.ToLower(strings.TrimSpace(region))
	if region == "" {
		region = defaultRegion
	}
	return fmt.Sprintf("https://management.%s.kiro.dev", region)
}

// BuildListModelsURL 拼出 ListAvailableModels 地址。
//
// 寻址方式是 AWS JSON 1.0 风格——POST 根路径 + x-amz-target 头，不是像
// ListAvailableProfiles 那样在路径后缀带操作名；这一点和
// generateAssistantResponse 的 CodeWhisperer/Q 端点（EndpointsFor）是同一种
// 寻址方式，与 management host 本身无关，是 AWS 在同一账号体系下两种不同
// 的 REST 约定并存。
func BuildListModelsURL(region string) string {
	return ListModelsHostFor(region) + "/"
}

// BuildListModelsRequestBody 构造请求体。
//
// ⚠️ "profileArn" 这个顶层 JSON 字段名是按同一 AWS 服务下
// generateAssistantResponse.Request 的既有字段命名类推得出，不是抓包确认——
// 2026-09-06 那次真实账号验证是手动调用完成的，没有把请求/响应抓包写进
// 代码库或提交记录（见 ListModelsHostFor 的说明）。字段名如果不对，AWS 对
// 未知/错误字段的一贯行为是返回明确的 400 ValidationException，不会静默
// 接受并返回错乱数据——调用方（AccountTestService.fetchKiroUpstreamModels）
// 已经把这类失败当作可见错误呈现给管理员。上线前应尽量用真实账号验证一次。
func BuildListModelsRequestBody(profileArn string) ([]byte, error) {
	body := map[string]any{}
	if arn := strings.TrimSpace(profileArn); arn != "" {
		body["profileArn"] = arn
	}
	return json.Marshal(body)
}

// ModelInfo 是 ListAvailableModels 返回的一条模型信息。
type ModelInfo struct {
	ID   string
	Name string
}

// rawListModelsResponse 镜像上游响应的可能形状。同一份数据在字段名上留了
// 多个候选（modelId/id/modelIdentifier，modelName/displayName/name），
// 因为响应体的确切字段名同样是类推而非抓包确认（见 BuildListModelsRequestBody
// 的提醒）——尽量提高和真实响应对上的概率，而不是假设一种形状、解析失败
// 就返回 0 条。0 条会被上层明确判成"上游没有返回可用模型"这个可见错误
// （不会伪装成功，见 fetchKiroUpstreamModels），所以这里"多试几个候选字段"
// 的下行风险有限，上行收益是省一次真机试错。
type rawListModelsResponse struct {
	Models []struct {
		ModelID         string `json:"modelId"`
		ID              string `json:"id"`
		ModelIdentifier string `json:"modelIdentifier"`
		ModelName       string `json:"modelName"`
		DisplayName     string `json:"displayName"`
		Name            string `json:"name"`
	} `json:"models"`
}

// ParseListModelsResponse 解析 ListAvailableModels 响应，按上游返回顺序
// 保留结果（不重排、不去重——去重留给调用方按需处理）。
func ParseListModelsResponse(raw []byte) ([]ModelInfo, error) {
	var r rawListModelsResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("kiro: decode ListAvailableModels response: %w", err)
	}

	out := make([]ModelInfo, 0, len(r.Models))
	for _, m := range r.Models {
		id := strings.TrimSpace(firstNonEmpty(m.ModelID, m.ID, m.ModelIdentifier))
		if id == "" {
			continue
		}
		name := strings.TrimSpace(firstNonEmpty(m.ModelName, m.DisplayName, m.Name))
		out = append(out, ModelInfo{ID: id, Name: name})
	}
	return out, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
