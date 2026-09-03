//go:build unit

package kiro

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// TestCredentialStashLocalOnlySurvivesRedisRecovery mirrors
// TestSessionStoreLocalOnlySurvivesRedisRecovery: Redis is unreachable
// exactly while Set() runs (falls back to memory, marked localOnly), then
// recovers before the admin clicks "I've completed authorization" in the
// panel. Because that stash entry was never actually written to Redis, a
// naive query against the now-reachable Redis truthfully answers "not
// found". TakeOnce must still resolve — and burn — the entry from memory via
// the localOnly marker, or a legitimate pending credential fetch gets
// rejected as if the callback never happened.
func TestCredentialStashLocalOnlySurvivesRedisRecovery(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })

	stash := NewRedisCredentialStash(client)
	defer stash.Stop()

	ctx := context.Background()
	creds := map[string]any{"access_token": "at", "profile_arn": "arn:x"}

	// Redis is unreachable exactly while Set() runs.
	mr.Close()
	stash.Set(ctx, "sid", creds)
	require.True(t, stash.isLocalOnly("sid"), "a failed Redis write must mark the stash entry localOnly")

	// Redis recovers before the admin's "I've completed authorization" poll
	// arrives. It never received "sid", so it will truthfully report "not
	// found" for it.
	require.NoError(t, mr.Restart())

	got, ok := stash.TakeOnce(ctx, "sid")
	require.True(t, ok, "TakeOnce must resolve a localOnly entry from memory, not from a reachable-but-empty Redis")
	require.Equal(t, "at", got["access_token"])
	require.Equal(t, "arn:x", got["profile_arn"])

	_, ok = stash.TakeOnce(ctx, "sid")
	require.False(t, ok, "a consumed localOnly entry must not be consumable twice")
}

// TestCredentialStashSuccessfulRedisWriteIsNotLocalOnly confirms an entry
// whose Redis write succeeded is not marked localOnly, so TakeOnce keeps
// trusting Redis as authoritative for it, and that TakeOnce actually deletes
// the remote copy (not just the local bookkeeping).
func TestCredentialStashSuccessfulRedisWriteIsNotLocalOnly(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })

	stash := NewRedisCredentialStash(client)
	defer stash.Stop()

	ctx := context.Background()
	stash.Set(ctx, "sid", map[string]any{"access_token": "remote-at"})
	require.False(t, stash.isLocalOnly("sid"), "a successful Redis write must not be marked localOnly")

	got, ok := stash.TakeOnce(ctx, "sid")
	require.True(t, ok)
	require.Equal(t, "remote-at", got["access_token"])

	// Second TakeOnce must fail — both against a fresh stash instance backed
	// by the same Redis (proves the remote copy was actually deleted, not
	// just this process's local view of it).
	other := NewRedisCredentialStash(client)
	defer other.Stop()
	_, ok = other.TakeOnce(ctx, "sid")
	require.False(t, ok, "TakeOnce must delete the remote copy so other replicas can't re-consume it")
}
