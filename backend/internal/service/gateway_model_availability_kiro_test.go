//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fork：anthropic 分组的调度池除了 antigravity 还会混入开了混合调度的 kiro 账号
// （mixedSchedulingPlatforms）。「模型不支持（404）还是暂时不可用（503）」的判断要看同一个池，
// 否则只有 kiro 账号支持的模型，在这些账号暂时不可用时会被报成 model_not_found。

func kiroDiagnosisAccount(id, groupID int64, platform string, mixed bool, models ...string) Account {
	mapping := make(map[string]any, len(models))
	for _, model := range models {
		mapping[model] = model
	}
	account := Account{
		ID:            id,
		Platform:      platform,
		Status:        StatusActive,
		Schedulable:   true,
		AccountGroups: []AccountGroup{{GroupID: groupID}},
		Credentials:   map[string]any{"model_mapping": mapping},
	}
	if mixed {
		account.Extra = map[string]any{"mixed_scheduling": true}
	}
	return account
}

func diagnoseForKiroTest(accounts []Account, groupID int64, model, platform string) ModelAvailabilityDiagnosis {
	repo := &mockAccountRepoForPlatform{accounts: accounts, accountsByID: map[int64]*Account{}}
	svc := &GatewayService{accountRepo: repo, cfg: testConfig()}
	return svc.DiagnoseModelAvailabilityForPlatform(context.Background(), &groupID, model, platform)
}

func TestDiagnoseModelAvailabilityForPlatform_AnthropicCountsMixedKiroAccounts(t *testing.T) {
	groupID := int64(77)
	cooldownUntil := time.Now().Add(time.Hour)
	kiro := kiroDiagnosisAccount(2, groupID, PlatformKiro, true, "claude-haiku-4-5")
	kiro.RateLimitResetAt = &cooldownUntil
	require.False(t, kiro.IsSchedulable(), "the Kiro account must be cooling down, as on the no-account error path")

	diag := diagnoseForKiroTest([]Account{
		kiroDiagnosisAccount(1, groupID, PlatformAnthropic, false, "claude-sonnet-4-5"),
		kiro,
	}, groupID, "claude-haiku-4-5", PlatformAnthropic)

	require.True(t, diag.HasAccountsInPool)
	require.True(t, diag.HasModelSupport, "a model only the mixed-scheduled Kiro account serves is configured — handler must stay on 503")
}

func TestDiagnoseModelAvailabilityForPlatform_AnthropicPoolOfOnlyMixedKiroAccounts(t *testing.T) {
	groupID := int64(77)

	diag := diagnoseForKiroTest([]Account{
		kiroDiagnosisAccount(1, groupID, PlatformKiro, true, "claude-haiku-4-5"),
	}, groupID, "claude-opus-4-8", PlatformAnthropic)

	require.True(t, diag.HasAccountsInPool, "mixed-scheduled Kiro accounts are part of the Anthropic pool")
	require.False(t, diag.HasModelSupport, "no account admits the model — handler should return 404")
}

func TestDiagnoseModelAvailabilityForPlatform_AnthropicIgnoresKiroWithoutMixedScheduling(t *testing.T) {
	groupID := int64(77)

	diag := diagnoseForKiroTest([]Account{
		kiroDiagnosisAccount(1, groupID, PlatformAnthropic, false, "claude-sonnet-4-5"),
		kiroDiagnosisAccount(2, groupID, PlatformKiro, false, "claude-haiku-4-5"),
	}, groupID, "claude-haiku-4-5", PlatformAnthropic)

	require.True(t, diag.HasAccountsInPool)
	require.False(t, diag.HasModelSupport, "a Kiro account without mixed scheduling is never scheduled for Anthropic groups")
}

func TestDiagnoseModelAvailabilityForPlatform_GeminiIgnoresMixedKiroAccounts(t *testing.T) {
	groupID := int64(77)

	diag := diagnoseForKiroTest([]Account{
		kiroDiagnosisAccount(1, groupID, PlatformGemini, false, "gemini-2.5-pro"),
		kiroDiagnosisAccount(2, groupID, PlatformKiro, true, "gemini-2.5-flash"),
	}, groupID, "gemini-2.5-flash", PlatformGemini)

	require.True(t, diag.HasAccountsInPool)
	require.False(t, diag.HasModelSupport, "Kiro only mixes into Anthropic groups")
}

// antigravity 的既有行为不变：没开混合调度的不算，开了的算。
func TestDiagnoseModelAvailabilityForPlatform_AnthropicMixedAntigravityUnchanged(t *testing.T) {
	groupID := int64(77)
	native := kiroDiagnosisAccount(1, groupID, PlatformAnthropic, false, "claude-sonnet-4-5")

	off := diagnoseForKiroTest([]Account{
		native,
		kiroDiagnosisAccount(2, groupID, PlatformAntigravity, false, "claude-opus-4-8"),
	}, groupID, "claude-opus-4-8", PlatformAnthropic)
	require.True(t, off.HasAccountsInPool)
	require.False(t, off.HasModelSupport)

	on := diagnoseForKiroTest([]Account{
		native,
		kiroDiagnosisAccount(2, groupID, PlatformAntigravity, true, "claude-opus-4-8"),
	}, groupID, "claude-opus-4-8", PlatformAnthropic)
	require.True(t, on.HasAccountsInPool)
	require.True(t, on.HasModelSupport)
}
