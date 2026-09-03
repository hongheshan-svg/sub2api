package kiro

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/redissession"
	"github.com/redis/go-redis/v9"
)

// credentialStashCleanupInterval 是内存回退存储的清理周期，与 SessionStore 一致。
const credentialStashCleanupInterval = time.Minute

// credentialStashEntry 是内存兜底路径里的一条暂存记录。
type credentialStashEntry struct {
	creds     map[string]any
	expiresAt time.Time
}

func (e *credentialStashEntry) expired() bool {
	return e == nil || (!e.expiresAt.IsZero() && time.Now().After(e.expiresAt))
}

// CredentialStash 是 IdC 授权码回调兑换出的账号 credentials 的一次性中转站。
//
// 背景：IdC 走浏览器整页跳转完成回调（Callback 渲染的是给人看的 HTML，不是
// JSON），前端 JS 拿不到 AWS 回传的 code，只能靠 Callback 把兑换结果暂存在
// 这里，再由管理员在面板里点「我已完成授权」触发一次轮询读取（FetchCredentials）
// 来拿到最终 credentials 并建号。
//
// 与 SessionStore 完全相同的多副本问题在这里同样成立：管理员点「我已完成
// 授权」这次请求可能落到任意一个副本，跟 Callback 落到哪个副本没有关系
// （见 session.go 顶部注释、Task 11 review），所以必须支持 Redis，且沿用
// 一模一样的 localOnly 设计——一旦某个 ID 被标记为 localOnly 就永远只信任
// 内存，不再去查 Redis，避免 Redis 从写入失败恢复可达后，对一个从未真正
// 写进去过的 ID 如实返回「不存在」，把内存里明明有效的暂存凭据误判为丢失。
type CredentialStash struct {
	mu        sync.RWMutex
	memory    map[string]*credentialStashEntry
	localOnly map[string]struct{}
	remote    *redissession.Store

	stopOnce sync.Once
	stopCh   chan struct{}
}

// NewCredentialStash 创建仅内存的暂存（单副本部署可用）。
func NewCredentialStash() *CredentialStash {
	s := &CredentialStash{
		memory:    make(map[string]*credentialStashEntry),
		localOnly: make(map[string]struct{}),
		stopCh:    make(chan struct{}),
	}
	go s.cleanupLoop()
	return s
}

// NewRedisCredentialStash 创建带 Redis 后端的暂存。
// 由 internal/service/wire.go 调用——service 包本身被 depguard 禁止 import redis。
// 键前缀与 SessionStore 的 "oauth:session:kiro:" 不同，避免两个存储互相冲突。
func NewRedisCredentialStash(rdb *redis.Client) *CredentialStash {
	s := NewCredentialStash()
	if rdb != nil {
		s.remote = redissession.New(rdb, "oauth:cred:kiro:", SessionTTL)
	}
	return s
}

// Set 写入暂存的 credentials。Redis 写失败（或压根没配置 Redis）时降级为
// 内存，并把这个 ID 标记为 localOnly——逻辑与 SessionStore.Set 完全一致。
func (s *CredentialStash) Set(ctx context.Context, id string, creds map[string]any) {
	if creds == nil {
		return
	}
	entry := &credentialStashEntry{creds: creds, expiresAt: time.Now().Add(SessionTTL)}

	var remoteErr error
	if s.remote != nil {
		if err := s.remote.Set(ctx, id, creds); err != nil {
			remoteErr = err
			slog.Warn("kiro credential stash redis write failed; falling back to memory", "error", err)
		}
	}

	s.mu.Lock()
	s.memory[id] = entry
	if s.remote == nil || remoteErr != nil {
		s.localOnly[id] = struct{}{}
	} else {
		delete(s.localOnly, id)
	}
	s.mu.Unlock()
}

// TakeOnce 读取并立即销毁暂存的 credentials（一次性）：不管值最终是从
// localOnly 内存、Redis 还是内存兜底路径解出的，返回前都会把 Redis 与
// 内存/localOnly 一并清空，保证同一个 id 的第二次调用永远返回 (nil, false)。
//
// 与 SessionStore.TryConsume 不同——那个方法只返回 bool，这里需要把值和
// 一次性保证放在同一次调用里，所以没有照搬 TryConsume，而是把读取与删除
// 写成一个方法。
func (s *CredentialStash) TakeOnce(ctx context.Context, id string) (map[string]any, bool) {
	var (
		creds map[string]any
		found bool
	)

	switch {
	case s.isLocalOnly(id):
		creds, found = s.getMemory(id)
	case s.remote != nil:
		var remoteCreds map[string]any
		if ok, err := s.remote.Get(ctx, id, &remoteCreds); err == nil && ok {
			creds, found = remoteCreds, true
		} else {
			creds, found = s.getMemory(id)
		}
	default:
		creds, found = s.getMemory(id)
	}

	// 无论值从哪条路径解出，都要把两条后端都烧掉——保证第二次调用绝不可能成功。
	if s.remote != nil {
		_ = s.remote.Delete(ctx, id)
	}
	s.mu.Lock()
	delete(s.memory, id)
	delete(s.localOnly, id)
	s.mu.Unlock()

	return creds, found
}

func (s *CredentialStash) getMemory(id string) (map[string]any, bool) {
	s.mu.RLock()
	entry, ok := s.memory[id]
	s.mu.RUnlock()
	if !ok || entry.expired() {
		return nil, false
	}
	return entry.creds, true
}

func (s *CredentialStash) isLocalOnly(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.localOnly[id]
	return ok
}

// Stop 结束后台清理。重复调用安全。
func (s *CredentialStash) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
}

func (s *CredentialStash) cleanupLoop() {
	ticker := time.NewTicker(credentialStashCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.mu.Lock()
			for id, entry := range s.memory {
				if entry.expired() {
					delete(s.memory, id)
					delete(s.localOnly, id)
				}
			}
			s.mu.Unlock()
		}
	}
}
