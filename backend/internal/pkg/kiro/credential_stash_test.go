package kiro

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCredentialStashSetAndTakeOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stash := NewCredentialStash()
	defer stash.Stop()

	creds := map[string]any{"access_token": "at", "refresh_token": "rt"}
	stash.Set(ctx, "sid-1", creds)

	got, ok := stash.TakeOnce(ctx, "sid-1")
	require.True(t, ok)
	require.Equal(t, "at", got["access_token"])
	require.Equal(t, "rt", got["refresh_token"])
}

// TestCredentialStashTakeOnceIsSingleUse 保证暂存的 credentials 只能被读取
// 一次——管理员的「我已完成授权」按钮可能因为网络重试被多次点击，第二次
// 读取绝不能再拿到（更不能拿到不同副本各自缓存的）同一份凭据。
func TestCredentialStashTakeOnceIsSingleUse(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stash := NewCredentialStash()
	defer stash.Stop()

	stash.Set(ctx, "sid", map[string]any{"access_token": "at"})

	_, ok := stash.TakeOnce(ctx, "sid")
	require.True(t, ok, "首次读取应成功")

	_, ok = stash.TakeOnce(ctx, "sid")
	require.False(t, ok, "重复读取必须失败")
}

func TestCredentialStashTakeOnceUnknownID(t *testing.T) {
	t.Parallel()

	stash := NewCredentialStash()
	defer stash.Stop()

	_, ok := stash.TakeOnce(context.Background(), "never-existed")
	require.False(t, ok)
}

func TestCredentialStashSetNilCredsIsNoop(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stash := NewCredentialStash()
	defer stash.Stop()

	stash.Set(ctx, "sid", nil)

	_, ok := stash.TakeOnce(ctx, "sid")
	require.False(t, ok, "nil creds 不应该被写入")
}

// TestCredentialStashExpiredEntryIsNotReturned 是白盒测试：直接往内存表里插
// 一条已过期的记录（同包访问未导出字段，与 SessionStore 那边的先例一致），
// 验证 TakeOnce 把它当作不存在处理。
func TestCredentialStashExpiredEntryIsNotReturned(t *testing.T) {
	t.Parallel()

	stash := NewCredentialStash()
	defer stash.Stop()

	stash.mu.Lock()
	stash.memory["old"] = &credentialStashEntry{
		creds:     map[string]any{"access_token": "at"},
		expiresAt: time.Now().Add(-time.Minute),
	}
	stash.localOnly["old"] = struct{}{}
	stash.mu.Unlock()

	_, ok := stash.TakeOnce(context.Background(), "old")
	require.False(t, ok, "过期记录不得返回")
}

// TestCredentialStashTakeOnceIsAtomicUnderConcurrency 证明 TakeOnce 的
// check-and-claim-and-delete 是真正原子的：管理员的「我已完成授权」按钮
// 可能因为网络重试在极短时间内被打出两个几乎同时到达的请求，两者都可能
// 落在同一个进程（同一个 CredentialStash 实例）上。如果读（check）和删
// （claim）不是同一次持锁操作，两个并发调用可以都在对方执行删除之前读到
// "还在"，都拿到 found == true 与同一份真实 OAuth credentials——这正是本
// 轮修复要消灭的竞态。用 -race 跑，既验证「恰好一个调用者拿到值」，也让
// Go 的 race detector 有机会抓到任何未被单次锁保护的读写。
func TestCredentialStashTakeOnceIsAtomicUnderConcurrency(t *testing.T) {
	t.Parallel()

	const goroutines = 50

	ctx := context.Background()
	stash := NewCredentialStash()
	defer stash.Stop()

	want := map[string]any{"access_token": "at", "refresh_token": "rt"}
	stash.Set(ctx, "sid-concurrent", want)

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		foundN   int
		gotValue map[string]any
	)

	wg.Add(goroutines)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			creds, ok := stash.TakeOnce(ctx, "sid-concurrent")
			if ok {
				mu.Lock()
				foundN++
				gotValue = creds
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	require.Equal(t, 1, foundN, "并发调用里必须有且仅有一个成功拿到 credentials")
	require.Equal(t, "at", gotValue["access_token"])
	require.Equal(t, "rt", gotValue["refresh_token"])
}

func TestCredentialStashStopIsIdempotent(t *testing.T) {
	t.Parallel()

	stash := NewCredentialStash()
	require.NotPanics(t, func() {
		stash.Stop()
		stash.Stop()
	})
}
