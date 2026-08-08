package proxyutil

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// SOCKS5 隧道建立（TCP 连接 + SOCKS 协商）必须有兜底超时：请求 ctx 通常没有
// deadline（流式请求随客户端断开才取消），代理黑洞时隧道建立会无限挂起。
func TestWithEstablishTimeout_AppliesDeadline(t *testing.T) {
	var captured context.Context
	dial := withEstablishTimeout(func(ctx context.Context, network, addr string) (net.Conn, error) {
		captured = ctx
		return nil, errors.New("intercepted")
	})

	_, err := dial(context.Background(), "tcp", "example.com:443")
	require.Error(t, err)
	require.NotNil(t, captured)

	deadline, ok := captured.Deadline()
	require.True(t, ok, "无 deadline 的 ctx 必须补隧道建立超时")
	require.WithinDuration(t, time.Now().Add(tunnelEstablishTimeout), deadline, 2*time.Second)
}

// 调用方已有更短 deadline 时不得放宽。
func TestWithEstablishTimeout_KeepsShorterCallerDeadline(t *testing.T) {
	var captured context.Context
	dial := withEstablishTimeout(func(ctx context.Context, network, addr string) (net.Conn, error) {
		captured = ctx
		return nil, errors.New("intercepted")
	})

	callerDeadline := time.Now().Add(500 * time.Millisecond)
	ctx, cancel := context.WithDeadline(context.Background(), callerDeadline)
	defer cancel()

	_, _ = dial(ctx, "tcp", "example.com:443")

	deadline, ok := captured.Deadline()
	require.True(t, ok)
	require.False(t, deadline.After(callerDeadline.Add(50*time.Millisecond)),
		"包络不得放宽调用方已有的更短 deadline")
}

// ConfigureTransportProxy 的 SOCKS5 分支必须应用隧道建立超时包络。
func TestConfigureTransportProxy_SOCKS5DialHasEstablishDeadline(t *testing.T) {
	// 一个接受连接但永不回应的本地"代理"，模拟黑洞。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				buf := make([]byte, 1024)
				for {
					if _, err := c.Read(buf); err != nil {
						_ = c.Close()
						return
					}
				}
			}(conn)
		}
	}()

	httpTransport := &http.Transport{}
	proxyURL, err := url.Parse("socks5://" + ln.Addr().String())
	require.NoError(t, err)
	require.NoError(t, ConfigureTransportProxy(httpTransport, proxyURL))
	require.NotNil(t, httpTransport.DialContext)

	// 带短 deadline 的 ctx 必须被尊重（挂起代理下快速失败，而非无限等待）。
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := httpTransport.DialContext(ctx, "tcp", "example.com:443")
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err, "挂起的 SOCKS5 代理必须返回错误")
	case <-time.After(3 * time.Second):
		t.Fatal("SOCKS5 隧道建立未受 ctx deadline 约束（挂起）")
	}
}

var errStub = errors.New("stub dial")

// 回归：SOCKS5 分支覆盖了调用方在 Transport 上设置的 DialContext，
// 底层 forward dialer 必须自带建连超时。proxy.Direct 是零值 net.Dialer，
// 代理不可达时会一直卡到内核 TCP 重传耗尽（Linux 约 130 秒）。
func TestSOCKS5ForwardDialerHasBoundedTimeout(t *testing.T) {
	require.Greater(t, socks5ForwardDialer.Timeout, time.Duration(0))
	require.Equal(t, socks5DialTimeout, socks5ForwardDialer.Timeout)
	require.Equal(t, socks5DialKeepAlive, socks5ForwardDialer.KeepAlive)
}

func TestConfigureTransportProxySOCKS5SetsDialContext(t *testing.T) {
	for _, scheme := range []string{"socks5", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			proxyURL, err := url.Parse(scheme + "://127.0.0.1:1080")
			require.NoError(t, err)

			transport := &http.Transport{}
			require.NoError(t, ConfigureTransportProxy(transport, proxyURL))
			require.NotNil(t, transport.DialContext)
			require.Nil(t, transport.Proxy, "SOCKS5 不应设置 Transport.Proxy")
		})
	}
}

// HTTP 代理走 Transport.Proxy，不得覆盖调用方设置的 DialContext。
func TestConfigureTransportProxyHTTPPreservesDialContext(t *testing.T) {
	proxyURL, err := url.Parse("http://127.0.0.1:8080")
	require.NoError(t, err)

	called := false
	transport := &http.Transport{}
	transport.DialContext = func(_ context.Context, _, _ string) (net.Conn, error) {
		called = true
		return nil, errStub
	}

	require.NoError(t, ConfigureTransportProxy(transport, proxyURL))
	require.NotNil(t, transport.Proxy)
	require.NotNil(t, transport.DialContext)

	_, _ = transport.DialContext(context.Background(), "tcp", "127.0.0.1:1")
	require.True(t, called, "HTTP 代理分支不应替换调用方的 DialContext")
}
