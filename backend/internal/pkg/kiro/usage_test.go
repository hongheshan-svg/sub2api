package kiro

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const sampleUsageLimits = `{
  "nextDateReset": 1789000000,
  "subscriptionInfo": {"subscriptionTitle": "KIRO PRO+", "overageCapability": "OVERAGE_CAPABLE"},
  "overageConfiguration": {"overageStatus": "ENABLED"},
  "usageBreakdownList": [
    {
      "resourceType": "AGENTIC_REQUEST",
      "currentUsage": 120,
      "currentUsageWithPrecision": 120.5,
      "usageLimit": 1000,
      "usageLimitWithPrecision": 1000.0,
      "nextDateReset": 1789000000,
      "overageCap": 50.0,
      "overageRate": 0.04,
      "currentOverages": 1.2,
      "bonuses": [
        {"currentUsage": 0, "usageLimit": 200, "status": "ACTIVE"},
        {"currentUsage": 50, "usageLimit": 50, "status": "EXPIRED"}
      ]
    },
    {"resourceType": "CODE_COMPLETION", "currentUsage": 5, "usageLimit": 100}
  ]
}`

func TestParseUsageLimits(t *testing.T) {
	t.Parallel()

	u, err := ParseUsageLimits([]byte(sampleUsageLimits))
	require.NoError(t, err)
	require.Equal(t, "KIRO PRO+", u.SubscriptionTitle)
	require.Equal(t, "ENABLED", u.OverageStatus)
	require.Equal(t, "OVERAGE_CAPABLE", u.OverageCapability)
	require.NotNil(t, u.NextDateReset)
	require.Len(t, u.Breakdowns, 2)
}

func TestUsageLimitsAgenticRequestPicksRightBreakdown(t *testing.T) {
	t.Parallel()

	u, err := ParseUsageLimits([]byte(sampleUsageLimits))
	require.NoError(t, err)

	b := u.AgenticRequest()
	require.NotNil(t, b)
	require.Equal(t, "AGENTIC_REQUEST", b.ResourceType)
	require.InDelta(t, 120.5, b.CurrentUsage, 1e-9, "有精确值时优先用精确值")
	require.InDelta(t, 1000.0, b.UsageLimit, 1e-9)
	require.NotNil(t, b.NextDateReset)
}

// TestEffectiveLimitIncludesActiveBonuses 覆盖一个易错点：
// 只看 usageLimit 会把有赠送额度的账号误判为已耗尽。
func TestEffectiveLimitIncludesActiveBonuses(t *testing.T) {
	t.Parallel()

	u, err := ParseUsageLimits([]byte(sampleUsageLimits))
	require.NoError(t, err)

	b := u.AgenticRequest()
	// 1000 基础 + 200 ACTIVE 赠送；EXPIRED 的 50 不计。
	require.InDelta(t, 1200.0, b.EffectiveLimit(), 1e-9)
}

func TestExhaustedUsesEffectiveLimit(t *testing.T) {
	t.Parallel()

	b := &UsageBreakdown{CurrentUsage: 1100, UsageLimit: 1000,
		Bonuses: []Bonus{{UsageLimit: 200, Status: "ACTIVE"}}}
	require.False(t, b.Exhausted(), "1100 < 1000+200，未耗尽")

	b.CurrentUsage = 1200
	require.True(t, b.Exhausted())
}

func TestUtilizationPercent(t *testing.T) {
	t.Parallel()

	b := &UsageBreakdown{CurrentUsage: 600, UsageLimit: 1000}
	require.InDelta(t, 60.0, b.UtilizationPercent(), 1e-9)

	// 零额度不得除零。
	require.Zero(t, (&UsageBreakdown{CurrentUsage: 5}).UtilizationPercent())
}

func TestParseUsageLimitsHandlesMissingFields(t *testing.T) {
	t.Parallel()

	u, err := ParseUsageLimits([]byte(`{}`))
	require.NoError(t, err)
	require.Empty(t, u.SubscriptionTitle)
	require.Nil(t, u.AgenticRequest())

	_, err = ParseUsageLimits([]byte(`not json`))
	require.Error(t, err)
}

func TestBuildUsageLimitsURL(t *testing.T) {
	t.Parallel()

	got := BuildUsageLimitsURL("https://q.us-east-1.amazonaws.com", "arn:aws:x:::profile/A B")
	require.Contains(t, got, "/getUsageLimits?")
	require.Contains(t, got, "origin=AI_EDITOR")
	require.Contains(t, got, "resourceType=AGENTIC_REQUEST")
	require.Contains(t, got, "isEmailRequired=true")
	require.Contains(t, got, "profileArn=arn%3Aaws%3Ax%3A%3A%3Aprofile%2FA+B",
		"profileArn 必须 URL 编码")

	// 无 profileArn（API Key 账号）时不带该参数。
	require.NotContains(t, BuildUsageLimitsURL("https://q.us-east-1.amazonaws.com", ""), "profileArn=")
}
