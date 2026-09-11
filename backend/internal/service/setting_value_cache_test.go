//go:build unit

package service

import (
	"context"
	"testing"

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
