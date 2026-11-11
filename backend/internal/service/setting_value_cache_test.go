//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// 回归测试（MW-2）：会话绑定开关 / 站点名称等"单 key 只读 setting"此前每次调用都直接
// 查库；改造后应在 settingValueCacheTTL 内命中进程内缓存，不再重复查库。
func TestSettingService_SimpleValueGettersAreCached(t *testing.T) {
	repo := &settingRepoStub{values: map[string]string{
		SettingKeySessionBindingEnabled: "true",
		SettingKeySiteName:              "MySite",
	}}
	svc := NewSettingService(repo, &config.Config{})
	ctx := context.Background()

	require.True(t, svc.IsSessionBindingEnabled(ctx))
	require.True(t, svc.IsSessionBindingEnabled(ctx))
	require.True(t, svc.IsSessionBindingEnabled(ctx))
	require.Equal(t, 1, repo.getValueCalls, "3 次调用应只查一次 DB，其余命中缓存")

	require.Equal(t, "MySite", svc.GetSiteName(ctx))
	require.Equal(t, "MySite", svc.GetSiteName(ctx))
	require.Equal(t, 2, repo.getValueCalls, "不同 key 各自独立缓存；GetSiteName 首次调用单独查一次 DB")
}

// 回归测试（MW-2）：DB 查询失败时应回退到各 getter 自身的默认值语义，
// 且失败结果本身也应被短 TTL 缓存住（不放大对故障 DB 的查询压力）。
func TestSettingService_SimpleValueGetter_FallsBackOnError(t *testing.T) {
	repo := &settingRepoStub{err: context.DeadlineExceeded}
	svc := NewSettingService(repo, &config.Config{})
	ctx := context.Background()

	require.False(t, svc.IsSessionBindingEnabled(ctx))
	require.False(t, svc.IsSessionBindingEnabled(ctx))
	require.Equal(t, "Sub2API", svc.GetSiteName(ctx))
	require.Equal(t, 2, repo.getValueCalls, "session_binding 缓存命中 1 次查询；site_name 单独 1 次查询")
}

// 回归测试（MW-2）：写入设置后应立即刷新缓存，当前节点下一次读取直接拿到新值，
// 不需要等 TTL 过期，也不需要再打一次 DB。
func TestSettingService_RefreshCachedSettingsPopulatesSimpleValueCache(t *testing.T) {
	repo := &settingRepoStub{values: map[string]string{
		SettingKeySiteName: "Old",
	}}
	svc := NewSettingService(repo, &config.Config{})
	ctx := context.Background()

	require.Equal(t, "Old", svc.GetSiteName(ctx))
	require.Equal(t, 1, repo.getValueCalls)

	svc.refreshCachedSettings(&SystemSettings{SiteName: "New"})

	require.Equal(t, "New", svc.GetSiteName(ctx), "写入后应立即读到新值")
	require.Equal(t, 1, repo.getValueCalls, "写入刷新缓存后不应再触发 DB 查询")
}

// expireCachedSettingValue 把某个 key 的缓存条目改成已过期（保留其值），模拟 TTL 到期。
func expireCachedSettingValue(t *testing.T, svc *SettingService, key string) {
	t.Helper()
	v, ok := svc.settingValueCache.Load(key)
	require.True(t, ok, "缓存里应已有 %s", key)
	entry, ok := v.(cachedSettingValueEntry)
	require.True(t, ok)
	entry.expiresAt = time.Now().Add(-time.Second).UnixNano()
	svc.settingValueCache.Store(key, entry)
}

func cachedSettingValueRemaining(t *testing.T, svc *SettingService, key string) time.Duration {
	t.Helper()
	v, ok := svc.settingValueCache.Load(key)
	require.True(t, ok, "缓存里应已有 %s", key)
	entry, ok := v.(cachedSettingValueEntry)
	require.True(t, ok)
	return time.Duration(entry.expiresAt - time.Now().UnixNano())
}

// 回归测试：TTL 到期后那一次刷新如果撞上 DB 抖动，不能把"查询失败"当成"未配置"缓存住——
// 否则会话绑定（每个 JWT 请求都读的安全开关）会静默关闭一整个 TTL。应沿用上一次确认过的值，
// 只做短退避，DB 恢复后立即读到真实值。
func TestSettingService_SimpleValueGetter_KeepsLastKnownValueOnTransientError(t *testing.T) {
	repo := &settingRepoStub{values: map[string]string{
		SettingKeySessionBindingEnabled: "true",
		SettingKeySiteName:              "MySite",
	}}
	svc := NewSettingService(repo, &config.Config{})
	ctx := context.Background()

	require.True(t, svc.IsSessionBindingEnabled(ctx))
	require.Equal(t, "MySite", svc.GetSiteName(ctx))
	require.Equal(t, 2, repo.getValueCalls)

	expireCachedSettingValue(t, svc, SettingKeySessionBindingEnabled)
	expireCachedSettingValue(t, svc, SettingKeySiteName)
	repo.err = context.DeadlineExceeded

	require.True(t, svc.IsSessionBindingEnabled(ctx), "DB 故障期间应沿用上一次确认过的值")
	require.Equal(t, "MySite", svc.GetSiteName(ctx), "DB 故障期间不应回退成默认站点名")
	require.Equal(t, 4, repo.getValueCalls)

	require.True(t, svc.IsSessionBindingEnabled(ctx))
	require.Equal(t, 4, repo.getValueCalls, "退避窗口内不应重复查询故障中的 DB")
	require.LessOrEqual(t, cachedSettingValueRemaining(t, svc, SettingKeySessionBindingEnabled), settingValueErrorBackoff,
		"失败结果只应退避 settingValueErrorBackoff，而不是完整 TTL")

	// DB 恢复且值已被改掉：退避结束后读到的是新值，而不是继续沿用旧值。
	repo.err = nil
	repo.values[SettingKeySessionBindingEnabled] = "false"
	expireCachedSettingValue(t, svc, SettingKeySessionBindingEnabled)
	require.False(t, svc.IsSessionBindingEnabled(ctx))
	require.Equal(t, 5, repo.getValueCalls)
}

// 回归测试：没有任何旧值时查询失败仍回退默认值，但只退避很短时间；
// "key 确实不存在"则是确定性结果，按完整 TTL 缓存。
func TestSettingService_SimpleValueGetter_ErrorBackoffVsNotFoundTTL(t *testing.T) {
	ctx := context.Background()

	failing := NewSettingService(&settingRepoStub{err: context.DeadlineExceeded}, &config.Config{})
	require.False(t, failing.IsSessionBindingEnabled(ctx))
	require.LessOrEqual(t, cachedSettingValueRemaining(t, failing, SettingKeySessionBindingEnabled), settingValueErrorBackoff)

	missingRepo := &settingRepoStub{values: map[string]string{}}
	missing := NewSettingService(missingRepo, &config.Config{})
	require.Equal(t, "Sub2API", missing.GetSiteName(ctx))
	require.Equal(t, "Sub2API", missing.GetSiteName(ctx))
	require.Equal(t, 1, missingRepo.getValueCalls, "未配置的 key 也应被缓存")
	require.Greater(t, cachedSettingValueRemaining(t, missing, SettingKeySiteName), settingValueErrorBackoff,
		"未配置是确定性结果，应按完整 TTL 缓存")
}
