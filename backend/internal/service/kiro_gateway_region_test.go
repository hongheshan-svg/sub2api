//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestKiroDataPlaneRegionPrefersProfileArnRegion 覆盖 kiroDataPlaneRegion 的
// 核心行为：profileArn 里的区域优先于账号存的 SSO 授权区域——见其文档，
// 移植自参考实现 Kiro-Go 记录的坑（两者可能不是同一个区域）。
func TestKiroDataPlaneRegionPrefersProfileArnRegion(t *testing.T) {
	acc := kiroAccount(map[string]any{
		"auth_method": "idc",
		"region":      "us-east-1",
		"profile_arn": "arn:aws:codewhisperer:eu-central-1:123456789012:profile/abcdef123456",
	})
	require.Equal(t, "eu-central-1", kiroDataPlaneRegion(acc))
}

func TestKiroDataPlaneRegionFallsBackToAccountRegionWhenProfileArnMissing(t *testing.T) {
	acc := kiroAccount(map[string]any{
		"auth_method": "idc",
		"region":      "eu-central-1",
	})
	require.Equal(t, "eu-central-1", kiroDataPlaneRegion(acc))
}

func TestKiroDataPlaneRegionFallsBackToAccountRegionWhenProfileArnMalformed(t *testing.T) {
	acc := kiroAccount(map[string]any{
		"auth_method": "idc",
		"region":      "eu-central-1",
		"profile_arn": "not-an-arn",
	})
	require.Equal(t, "eu-central-1", kiroDataPlaneRegion(acc))
}

func TestKiroDataPlaneRegionNilAccount(t *testing.T) {
	require.Equal(t, "", kiroDataPlaneRegion(nil))
}
