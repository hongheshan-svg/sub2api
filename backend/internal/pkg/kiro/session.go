package kiro

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/redissession"
	"github.com/redis/go-redis/v9"
)

// SessionTTL 是一次授权流程的存活时间。
// AWS 的设备码通常 10 分钟过期，授权码流程也在同一量级。
const SessionTTL = 10 * time.Minute

// sessionCleanupInterval 是内存回退存储的清理周期。
const sessionCleanupInterval = time.Minute

// OAuthSession 保存一次进行中的授权流程状态。
//
// 注意：它带 ClientSecret，属于敏感数据。Redis 键有 TTL，消费后立即删除。
type OAuthSession struct {
	Method       AuthMethod `json:"method"`
	ClientID     string     `json:"client_id"`
	ClientSecret string     `json:"client_secret"`
	// Verifier 是 PKCE 的 code_verifier（仅 idc 路径）。
	Verifier string `json:"verifier"`
	// State 用于校验回调，防 CSRF（仅 idc 路径）。
	State       string `json:"state"`
	Region      string `json:"region"`
	IssuerURL   string `json:"issuer_url"`
	RedirectURI string `json:"redirect_uri"`
	// DeviceCode 仅 builder_id 路径使用。
	DeviceCode string    `json:"device_code"`
	Interval   int       `json:"interval"`
	ExpiresAt  time.Time `json:"expires_at"`
}

func (s *OAuthSession) expired() bool {
	return !s.ExpiresAt.IsZero() && time.Now().After(s.ExpiresAt)
}

// SessionStore 管理授权会话，优先用 Redis，失败时回退进程内存。
//
// 必须支持 Redis：IdC 与 social 走自建回调页，多副本部署时浏览器回调
// 可能落到另一个副本，进程内存里的会话会直接丢失。
type SessionStore struct {
	mu     sync.RWMutex
	memory map[string]*OAuthSession
	remote *redissession.Store

	stopOnce sync.Once
	stopCh   chan struct{}
}

// NewSessionStore 创建仅内存的存储（单副本部署可用）。
func NewSessionStore() *SessionStore {
	s := &SessionStore{
		memory: make(map[string]*OAuthSession),
		stopCh: make(chan struct{}),
	}
	go s.cleanupLoop()
	return s
}

// NewRedisSessionStore 创建带 Redis 后端的存储。
// 由 internal/service/wire.go 调用 —— service 包本身被 depguard 禁止 import redis。
func NewRedisSessionStore(rdb *redis.Client) *SessionStore {
	s := NewSessionStore()
	if rdb != nil {
		s.remote = redissession.New(rdb, "oauth:session:kiro:", SessionTTL)
	}
	return s
}

// Set 写入会话。Redis 写失败时降级为内存，保证单机仍可完成授权。
func (s *SessionStore) Set(ctx context.Context, id string, sess *OAuthSession) {
	if sess == nil {
		return
	}
	if sess.ExpiresAt.IsZero() {
		sess.ExpiresAt = time.Now().Add(SessionTTL)
	}

	if s.remote != nil {
		if err := s.remote.Set(ctx, id, sess); err == nil {
			return
		} else {
			slog.Warn("kiro oauth session redis write failed; falling back to memory", "error", err)
		}
	}

	s.mu.Lock()
	s.memory[id] = sess
	s.mu.Unlock()
}

// Get 读取会话，过期的视为不存在。
func (s *SessionStore) Get(ctx context.Context, id string) (*OAuthSession, bool) {
	if s.remote != nil {
		var sess OAuthSession
		if found, err := s.remote.Get(ctx, id, &sess); err == nil && found {
			if sess.expired() {
				return nil, false
			}
			return &sess, true
		}
	}

	s.mu.RLock()
	sess, ok := s.memory[id]
	s.mu.RUnlock()
	if !ok || sess.expired() {
		return nil, false
	}
	return sess, true
}

// Delete 删除会话。
func (s *SessionStore) Delete(ctx context.Context, id string) {
	if s.remote != nil {
		_ = s.remote.Delete(ctx, id)
	}
	s.mu.Lock()
	delete(s.memory, id)
	s.mu.Unlock()
}

// TryConsume 原子地把会话标记为已使用，返回是否是首次消费。
// 用于保证一个授权码只能兑换一次，防止回调 URL 被重放。
func (s *SessionStore) TryConsume(ctx context.Context, id string) bool {
	if s.remote != nil {
		if ok, err := s.remote.TryConsume(ctx, id); err == nil {
			return ok
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.memory[id]
	if !ok || sess.expired() {
		return false
	}
	delete(s.memory, id)
	return true
}

// Stop 结束后台清理。重复调用安全。
func (s *SessionStore) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
}

func (s *SessionStore) cleanupLoop() {
	ticker := time.NewTicker(sessionCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.mu.Lock()
			for id, sess := range s.memory {
				if sess.expired() {
					delete(s.memory, id)
				}
			}
			s.mu.Unlock()
		}
	}
}

// GenerateSessionID 生成一个 URL 安全的随机会话 ID。
func GenerateSessionID() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("kiro: generate session id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
