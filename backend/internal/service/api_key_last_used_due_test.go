//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAPIKeyService_LastUsedTouchDue(t *testing.T) {
	repo := &apiKeyRepoStub{}
	svc := &APIKeyService{apiKeyRepo: repo}

	require.False(t, svc.LastUsedTouchDue(0), "非法 keyID 永远不需要 touch")
	require.False(t, svc.LastUsedTouchDue(-1))
	require.False(t, (*APIKeyService)(nil).LastUsedTouchDue(123))

	require.True(t, svc.LastUsedTouchDue(123), "从未 touch 过的 key 应判定为到期")

	require.NoError(t, svc.TouchLastUsed(context.Background(), 123))
	require.False(t, svc.LastUsedTouchDue(123), "防抖窗口内不应再判定为到期")
	require.True(t, svc.LastUsedTouchDue(456), "防抖按 key 隔离")

	svc.lastUsedTouchL1.Store(int64(123), time.Now().Add(-time.Second))
	require.True(t, svc.LastUsedTouchDue(123), "防抖窗口过后应重新到期")
}

// 落库失败后进入失败退避窗口：这段时间内同样不应再为它起 goroutine。
func TestAPIKeyService_LastUsedTouchDue_RespectsFailureBackoff(t *testing.T) {
	repo := &apiKeyRepoStub{
		updateLastUsed: func(context.Context, int64, time.Time) error {
			return errors.New("db write failed")
		},
	}
	svc := &APIKeyService{apiKeyRepo: repo}

	require.Error(t, svc.TouchLastUsed(context.Background(), 789))
	require.False(t, svc.LastUsedTouchDue(789))
}
