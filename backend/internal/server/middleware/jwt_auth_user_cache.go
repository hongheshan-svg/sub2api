package middleware

import (
	"context"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// jwtAuthUserCacheTTL 是 JWT 鉴权路径用户信息缓存的存活时间。
//
// 背景：网关侧的 API Key 鉴权早就有 L1+L2+singleflight 的成熟缓存，但 JWT 用户鉴权
// （管理后台/用户面板）此前完全没有对应机制——UserService.GetByID 每次都直接查 users 表，
// 还顺带做一次头像表的独立查询，等于每个已登录请求触发 2 次同步 DB 查询。
//
// 这里用一个很短的只读 TTL 缓存兜底，不做主动失效（不像 API Key 缓存那样接了 pub/sub）：
// 用户被封禁/改密（TokenVersion 变化）/角色变更等，最坏情况下会有至多 jwtAuthUserCacheTTL
// 的生效延迟，换来同一用户短时间内重复请求时的零 DB 往返。TTL 刻意设置得很短，
// 把这个延迟窗口压缩到运营上可接受的量级；如果后续需要零延迟撤销，应改造成主动失效
// （参考 UserService.authCacheInvalidator 的做法）而不是缩短 TTL 到失去缓存意义。
const jwtAuthUserCacheTTL = 5 * time.Second

// jwtAuthUserCacheSweepThreshold 控制何时顺带清理过期条目，避免长期运行后 map 无界增长。
const jwtAuthUserCacheSweepThreshold = 2048

type jwtAuthUserCacheEntry struct {
	user      *service.User
	err       error
	expiresAt time.Time
}

// jwtAuthUserCache 用短 TTL 包装 jwtUserReader，仅供 JWT 鉴权中间件使用。
// 命中同一 *service.User 指针的多次读取（缓存命中期间）均为只读——调用方不得修改返回值。
type jwtAuthUserCache struct {
	inner jwtUserReader
	ttl   time.Duration

	mu      sync.Mutex
	entries map[int64]jwtAuthUserCacheEntry
}

func newJWTAuthUserCache(inner jwtUserReader, ttl time.Duration) *jwtAuthUserCache {
	return &jwtAuthUserCache{
		inner:   inner,
		ttl:     ttl,
		entries: make(map[int64]jwtAuthUserCacheEntry),
	}
}

func (c *jwtAuthUserCache) GetByID(ctx context.Context, id int64) (*service.User, error) {
	now := time.Now()

	c.mu.Lock()
	if entry, ok := c.entries[id]; ok && now.Before(entry.expiresAt) {
		c.mu.Unlock()
		return entry.user, entry.err
	}
	c.mu.Unlock()

	user, err := c.inner.GetByID(ctx, id)

	c.mu.Lock()
	c.entries[id] = jwtAuthUserCacheEntry{user: user, err: err, expiresAt: now.Add(c.ttl)}
	if len(c.entries) > jwtAuthUserCacheSweepThreshold {
		for k, v := range c.entries {
			if now.After(v.expiresAt) {
				delete(c.entries, k)
			}
		}
	}
	c.mu.Unlock()

	return user, err
}
