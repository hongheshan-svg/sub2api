package kiro

import (
	"math"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

// EstimateText 估算一段文本的 token 数。
//
// Kiro 的 meteringEvent 只给 credits 和真实 cache token，不给 input/output token，
// 而本仓库按 token 计费，因此这两项必须本地估算。公式移植自
// Kiro-Go/proxy/token_estimator.go，按字符类加权。经验误差 ±10-20%。
func EstimateText(s string) int {
	if s == "" {
		return 0
	}

	runes := []rune(s)
	n := len(runes)
	if n == 0 {
		return 0
	}
	if n < 5 {
		if est := int(math.Ceil(float64(n) / 3.0)); est > 1 {
			return est
		}
		return 1
	}

	var ascii, digits, symbols, nonASCII int
	for _, r := range runes {
		switch {
		case r >= 0x80:
			nonASCII++
		case r >= '0' && r <= '9':
			digits++
		case (r >= '!' && r <= '/') || (r >= ':' && r <= '@') ||
			(r >= '[' && r <= '`') || (r >= '{' && r <= '~'):
			symbols++
		default:
			ascii++
		}
	}

	est := int(math.Ceil(
		float64(ascii)/4.5 +
			float64(digits)/2.0 +
			float64(symbols)/1.5 +
			float64(nonASCII)/1.5,
	))
	if est < 1 {
		return 1
	}
	return est
}

// EstimateRequestInput 估算整个请求的 input token：system + 全部消息 + 工具声明。
func EstimateRequestInput(req *apicompat.AnthropicRequest) int {
	if req == nil {
		return 0
	}

	total := 0

	if system, err := FlattenSystem(req.System); err == nil {
		total += EstimateText(system)
	}

	// 消息按原始 JSON 估算：内容块的结构本身也占 token，
	// 且这样不会因某条消息解析失败而整体归零。
	for _, m := range req.Messages {
		total += EstimateText(string(m.Content))
	}

	for _, tool := range req.Tools {
		total += EstimateText(tool.Name)
		total += EstimateText(tool.Description)
		total += EstimateText(string(tool.InputSchema))
	}

	return total
}
