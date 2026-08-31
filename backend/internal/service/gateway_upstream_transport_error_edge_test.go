//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// Edge cases for handleUpstreamTransportError that the main contract tests in
// gateway_upstream_transport_error_test.go do not pin. Kept as regression
// coverage carried over from the fork's own transport-failover patch, which
// upstream superseded in 44003d7f6 (fix(gateway): Anthropic/Bedrock 传输层错误转
// failover + 持久故障临时摘除账号).

// TestHandleUpstreamTransportError_WrappedClientCanceledNoFailover pins that a
// context.Canceled wrapped inside another error is still recognized as a client
// disconnect: no failover, no eviction. The upstream contract test only covers
// a bare context.Canceled, but the real forward paths wrap it (e.g.
// `http request failed: %w`).
func TestHandleUpstreamTransportError_WrappedClientCanceledNoFailover(t *testing.T) {
	repo := &transportTempUnschedRepoStub{}
	s := &GatewayService{accountRepo: repo}
	c := newTransportErrorTestGin(t)
	account := &Account{ID: 78, Name: "acc", Platform: PlatformAnthropic}

	wrapped := fmt.Errorf("http request failed: %w", context.Canceled)
	err := s.handleUpstreamTransportError(context.Background(), c, account, wrapped, OpsUpstreamErrorEvent{})

	var failoverErr *UpstreamFailoverError
	if errors.As(err, &failoverErr) {
		t.Fatal("wrapped context.Canceled must not fail over to another account")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want wrapped context.Canceled passthrough", err)
	}
	if repo.calls != 0 {
		t.Fatalf("SetTempUnschedulable called %d times on wrapped client cancel, want 0", repo.calls)
	}
}

// TestHandleUpstreamTransportError_NilAccountRepoStillFailsOver pins that a
// durable fault with no account repository wired (nil) neither panics nor
// swallows the failover: the request must still survive on another account.
func TestHandleUpstreamTransportError_NilAccountRepoStillFailsOver(t *testing.T) {
	s := &GatewayService{accountRepo: nil}
	c := newTransportErrorTestGin(t)
	account := &Account{ID: 55, Name: "no-db", Platform: PlatformAnthropic}

	durable := errors.New(`Post "https://api.anthropic.com/v1/messages": connect: connection refused`)
	err := s.handleUpstreamTransportError(context.Background(), c, account, durable, OpsUpstreamErrorEvent{})

	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		t.Fatalf("nil accountRepo must not prevent failover, got %T: %v", err, err)
	}
}
