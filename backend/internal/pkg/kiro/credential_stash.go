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

// TakeOnce 原子地读取并销毁暂存的 credentials（check-and-claim-and-delete
// 是单次原子操作）：同一个 id 上任意数量的并发调用里，保证有且仅有一个能
// 拿到 (creds, true)，其余全部拿到 (nil, false)。
//
// 与 SessionStore.TryConsume 不同——那个方法只返回 bool，这里需要把值和
// 一次性保证放在同一次调用里，所以没有照搬 TryConsume 的签名；但两条后端
// 各自的原子性机制与 TryConsume 完全一致：
//   - localOnly / 无 Redis 的内存路径：check 与 delete 共享同一次 s.mu.Lock，
//     镜像 session.go 的 tryConsumeMemory，Go 的互斥锁天然保证并发调用互斥。
//   - Redis 路径：先调 s.remote.TryConsume 做原子 claim（SetNX 一个独立的
//     used 标记，只有第一个调用者能拿到 ok==true），赢得 claim 之后数据 key
//     本身还在，再单独 Get 取值、Delete 清理。绝不先 Get 再 Delete——那样两次
//     并发调用可能都在各自的 Delete 前完成 Get，都读到同一份 credentials。
func (s *CredentialStash) TakeOnce(ctx context.Context, id string) (map[string]any, bool) {
	if s.isLocalOnly(id) {
		return s.takeMemory(id)
	}

	if s.remote != nil {
		ok, err := s.remote.TryConsume(ctx, id)
		if err != nil {
			// Redis 暂时不可达——退回内存兜底路径，与 SessionStore.TryConsume
			// 遇到 remote 出错时的既有约定一致。
			return s.takeMemory(id)
		}
		if !ok {
			// Redis 可达，且明确说「已被消费，或这个 id 从未存在」——信任这个
			// 结果，不再退回内存兜底，避免把一份陈旧的本地副本当成有效凭据
			// 返回（远端可达时远端才是权威源）。
			return nil, false
		}

		// 赢得了 claim：TryConsume 只 SetNX 了一个独立的 used 标记，并没有
		// 删掉数据 key，所以这里数据仍然在，可以安全地把它取出来。
		var remoteCreds map[string]any
		found, getErr := s.remote.Get(ctx, id, &remoteCreds)
		_ = s.remote.Delete(ctx, id)

		// 顺手清理本地可能残留的副本——正常走到这个分支说明 isLocalOnly(id)
		// 是 false，理论上不该有本地副本，但防御性清理一下不会有坏处。
		s.mu.Lock()
		delete(s.memory, id)
		delete(s.localOnly, id)
		s.mu.Unlock()

		if getErr != nil || !found {
			// 赢得了 claim 但数据没了——不寻常的不一致状态，当「不存在」处理，
			// 不重试、不 panic。
			return nil, false
		}
		return remoteCreds, true
	}

	return s.takeMemory(id)
}

// takeMemory 是内存/localOnly 路径的原子 check-and-delete：整个检查与删除
// 过程只持有一次 s.mu.Lock，镜像 session.go 的 tryConsumeMemory。这保证并发
// 调用互斥——sync.Mutex 天然串行化，只有第一个拿到锁的调用者能看到条目仍然
// 存在，其余调用者要么看不到条目，要么看到的已经是删除后的状态。
func (s *CredentialStash) takeMemory(id string) (map[string]any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.memory[id]
	if !ok || entry.expired() {
		delete(s.memory, id)
		delete(s.localOnly, id)
		return nil, false
	}

	creds := entry.creds
	delete(s.memory, id)
	delete(s.localOnly, id)
	return creds, true
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
