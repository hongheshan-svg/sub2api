//go:build unit

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// fork：开了混合调度的 kiro 账号会混入 anthropic 的 mixed bucket（service.mixedSchedulingPlatforms），
// 所以这个 bucket 的进程内快照也要跟着 kiro 的平台修订号失效。漏掉的话，SetAccount 写入的
// kiro 账号状态（如模型限流）要等快照条目过期才看得到。
func TestSchedulerCacheSnapshotMemoKiroRevisionInvalidatesAnthropicMixedBucket(t *testing.T) {
	ctx := context.Background()
	cache, _, recorder := newSchedulerCacheMemoUnit(t, defaultSchedulerSnapshotWriteChunkSize)
	kiro := memoTestAccountOn(service.PlatformKiro, 321, "kiro")
	kiro.Extra = map[string]any{"mixed_scheduling": true}
	anthropic := memoTestAccount(322, "anthropic")
	gemini := memoTestAccountOn(service.PlatformGemini, 323, "gemini")

	anthropicMixed := service.SchedulerBucket{GroupID: 302, Platform: service.PlatformAnthropic, Mode: service.SchedulerModeMixed}
	anthropicSingle := service.SchedulerBucket{GroupID: 302, Platform: service.PlatformAnthropic, Mode: service.SchedulerModeSingle}
	anthropicForced := service.SchedulerBucket{GroupID: 302, Platform: service.PlatformAnthropic, Mode: service.SchedulerModeForced}
	geminiMixed := service.SchedulerBucket{GroupID: 302, Platform: service.PlatformGemini, Mode: service.SchedulerModeMixed}
	kiroSingle := service.SchedulerBucket{GroupID: 302, Platform: service.PlatformKiro, Mode: service.SchedulerModeSingle}
	memoPublish(t, cache, anthropicMixed, anthropic, kiro)
	memoPublish(t, cache, anthropicSingle, anthropic)
	memoPublish(t, cache, anthropicForced, anthropic)
	memoPublish(t, cache, geminiMixed, gemini)
	memoPublish(t, cache, kiroSingle, kiro)
	for _, bucket := range []service.SchedulerBucket{anthropicMixed, anthropicSingle, anthropicForced, geminiMixed, kiroSingle} {
		memoRead(t, cache, bucket)
	}

	kiro.Name = "kiro-renamed"
	require.NoError(t, cache.SetAccount(ctx, &kiro))

	got, reloaded := memoReadExpectHit(t, cache, recorder, anthropicMixed)
	require.True(t, reloaded, "the Anthropic mixed bucket contains mixed-scheduled Kiro accounts")
	require.Equal(t, "kiro-renamed", got[1].Name)
	got, reloaded = memoReadExpectHit(t, cache, recorder, kiroSingle)
	require.True(t, reloaded)
	require.Equal(t, "kiro-renamed", got[0].Name)
	_, reloaded = memoReadExpectHit(t, cache, recorder, anthropicSingle)
	require.False(t, reloaded, "single buckets never contain Kiro accounts")
	_, reloaded = memoReadExpectHit(t, cache, recorder, anthropicForced)
	require.False(t, reloaded, "forced buckets never contain Kiro accounts")
	_, reloaded = memoReadExpectHit(t, cache, recorder, geminiMixed)
	require.False(t, reloaded, "Kiro only mixes into Anthropic buckets")
}

// 修订号的键集合要和 service 层决定 bucket 成员的规则一致：mixed bucket 里能出现哪些平台的账号，
// 就得读哪些平台的修订号。
func TestSchedulerSnapshotTagKeysCoverMixedMemberPlatforms(t *testing.T) {
	revisionKeys := func(bucket service.SchedulerBucket) []string {
		// 前两个是 ready / active，其后是全局修订号和各平台修订号。
		return schedulerSnapshotTagKeys(bucket)[2:]
	}
	rev := schedulerAccountMetaRevisionKeyForPlatform

	tests := []struct {
		name     string
		platform string
		mode     string
		want     []string
	}{
		{"anthropic mixed", service.PlatformAnthropic, service.SchedulerModeMixed,
			[]string{schedulerAccountMetaRevisionKey, rev(service.PlatformAnthropic), rev(service.PlatformAntigravity), rev(service.PlatformKiro)}},
		{"gemini mixed", service.PlatformGemini, service.SchedulerModeMixed,
			[]string{schedulerAccountMetaRevisionKey, rev(service.PlatformGemini), rev(service.PlatformAntigravity)}},
		{"antigravity mixed", service.PlatformAntigravity, service.SchedulerModeMixed,
			[]string{schedulerAccountMetaRevisionKey, rev(service.PlatformAntigravity)}},
		{"anthropic single", service.PlatformAnthropic, service.SchedulerModeSingle,
			[]string{schedulerAccountMetaRevisionKey, rev(service.PlatformAnthropic)}},
		{"anthropic forced", service.PlatformAnthropic, service.SchedulerModeForced,
			[]string{schedulerAccountMetaRevisionKey, rev(service.PlatformAnthropic)}},
		{"kiro single", service.PlatformKiro, service.SchedulerModeSingle,
			[]string{schedulerAccountMetaRevisionKey, rev(service.PlatformKiro)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bucket := service.SchedulerBucket{GroupID: 303, Platform: tt.platform, Mode: tt.mode}
			require.Equal(t, tt.want, revisionKeys(bucket))
		})
	}
}
