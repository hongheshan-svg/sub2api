package service

import (
	"context"
	"time"
)

// settingValueCacheTTL 是简单只读 setting 值的进程内缓存时长。这些 getter 此前每次调用都
// 直接查 DB：IsSessionBindingEnabled 在每一个 JWT 请求路径上被调用；GetSiteName /
// GetSiteSubtitle / GetDocURL / GetFrontendURL 被公开、无鉴权的 /robots.txt /sitemap.xml
// /llms.txt 端点在每次访问时调用，容易被爬虫/扫描器高频命中。TTL 与本文件其它设置缓存
// （如 panelRateLimitCacheTTL）保持一致；写入侧通过 refreshCachedSettingsAfterWrite
// 主动失效，这里的 TTL 只是兜底上限，不是唯一的生效延迟来源。
const (
	settingValueCacheTTL    = 60 * time.Second
	settingValueDBTimeout   = 5 * time.Second
	settingValueSFKeyPrefix = "setting_value:"
)

type cachedSettingValueEntry struct {
	value     string
	found     bool // 对应 settingRepo.GetValue 是否成功返回（区分"确认为空字符串"与"查询失败"）
	expiresAt int64
}

func (e cachedSettingValueEntry) fresh(now int64) bool {
	return now < e.expiresAt
}

// getSettingValueCached 是"单 key 读取 settingRepo.GetValue，短 TTL 缓存"这一重复模式的
// 统一实现，供 IsSessionBindingEnabled / GetSiteName / GetSiteSubtitle / GetDocURL /
// GetFrontendURL 复用。found=false 时表示底层查询失败或未初始化，调用方应按自身语义
// 回退默认值（与直接调用 settingRepo.GetValue 出错时的行为一致）。
func (s *SettingService) getSettingValueCached(ctx context.Context, key string) (value string, found bool) {
	if s == nil || s.settingRepo == nil {
		return "", false
	}

	now := time.Now().UnixNano()
	if v, ok := s.settingValueCache.Load(key); ok {
		if entry, ok := v.(cachedSettingValueEntry); ok && entry.fresh(now) {
			return entry.value, entry.found
		}
	}

	result, _, _ := s.settingValueSF.Do(settingValueSFKeyPrefix+key, func() (any, error) {
		// 二次检查：排队等待 singleflight 的调用方可能已经有另一个 goroutine 把新值填好了。
		if v, ok := s.settingValueCache.Load(key); ok {
			if entry, ok := v.(cachedSettingValueEntry); ok && entry.fresh(time.Now().UnixNano()) {
				return entry, nil
			}
		}

		dbCtx := ctx
		if dbCtx == nil {
			dbCtx = context.Background()
		}
		// 独立 context：断开请求取消链，避免客户端断连导致这次查询结果不落缓存。
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(dbCtx), settingValueDBTimeout)
		defer cancel()

		v, err := s.settingRepo.GetValue(dbCtx, key)
		entry := cachedSettingValueEntry{expiresAt: time.Now().Add(settingValueCacheTTL).UnixNano()}
		if err == nil {
			entry.value = v
			entry.found = true
		}
		s.settingValueCache.Store(key, entry)
		return entry, nil
	})

	if entry, ok := result.(cachedSettingValueEntry); ok {
		return entry.value, entry.found
	}
	return "", false
}

// storeCachedSettingValue 用刚写入 DB 的新值直接刷新缓存，供 refreshCachedSettings 调用，
// 使当前节点保存设置后立刻生效，而不必等 settingValueCacheTTL 过期或多打一次 DB 查询。
// 先 Forget 再 Store：缩小"排队中的旧值查询在 Store 之后才落地覆盖新值"的竞态窗口，
// 与本文件内其它设置缓存（如 gatewayForwardingCache）的既有做法一致。
func (s *SettingService) storeCachedSettingValue(key, value string) {
	if s == nil {
		return
	}
	s.settingValueSF.Forget(settingValueSFKeyPrefix + key)
	s.settingValueCache.Store(key, cachedSettingValueEntry{
		value:     value,
		found:     true,
		expiresAt: time.Now().Add(settingValueCacheTTL).UnixNano(),
	})
}
