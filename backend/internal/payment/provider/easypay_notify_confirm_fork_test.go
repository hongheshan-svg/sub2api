package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

// fork: 支付FM 等兼容易支付规范的平台没有 api.php?act=order 查单接口，
// 实例可以通过 notifyConfirm=off 关闭回调入账前的上游查单确认。

func newNotifyConfirmModeEasyPay(t *testing.T, mode string) (*EasyPay, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"code":1,"out_trade_no":"sub2_ORDER1","trade_no":"2026101600001","money":"10.00","status":1}`))
	}))
	t.Cleanup(server.Close)
	config := map[string]string{
		"pid":       "1001",
		"pkey":      "pkey-1",
		"apiBase":   server.URL,
		"notifyUrl": "https://example.com/notify",
		"returnUrl": "https://example.com/return",
	}
	if mode != "" {
		config[easyPayNotifyConfirmKey] = mode
	}
	provider, err := NewEasyPay("test-instance", config)
	if err != nil {
		t.Fatalf("NewEasyPay: %v", err)
	}
	return provider, &calls
}

func TestEasyPayConfirmNotificationDisabledSkipsUpstreamQuery(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"off", " OFF "} {
		provider, calls := newNotifyConfirmModeEasyPay(t, mode)
		n := confirmTestNotification()
		n.TradeNo = ""
		if err := provider.ConfirmNotification(context.Background(), n); err != nil {
			t.Fatalf("mode %q: ConfirmNotification returned error: %v", mode, err)
		}
		if calls.Load() != 0 {
			t.Fatalf("mode %q: upstream query calls = %d, want 0", mode, calls.Load())
		}
		if n.TradeNo != "" {
			t.Fatalf("mode %q: notification must be left untouched, trade_no = %q", mode, n.TradeNo)
		}
	}
}

// 未配置、显式 query 以及无法识别的取值都按开启处理，只有明确的 off 才跳过。
func TestEasyPayConfirmNotificationQueriesUpstreamUnlessDisabled(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"", "query", "disabled", "false"} {
		provider, calls := newNotifyConfirmModeEasyPay(t, mode)
		if err := provider.ConfirmNotification(context.Background(), confirmTestNotification()); err != nil {
			t.Fatalf("mode %q: ConfirmNotification returned error: %v", mode, err)
		}
		if calls.Load() != 1 {
			t.Fatalf("mode %q: upstream query calls = %d, want 1", mode, calls.Load())
		}
	}
}

// 支付FM 签约类型的回调额外带 channel_order_no（参与签名）。
func TestEasyPayNotifyAcceptsChannelOrderNo(t *testing.T) {
	t.Parallel()
	e := easyPayPoCProvider()

	encode := func(params map[string]string) string {
		q := url.Values{}
		for k, v := range params {
			q.Set(k, v)
		}
		q.Set("sign", easyPaySign(params, e.config["pkey"]))
		q.Set("sign_type", signTypeMD5)
		return q.Encode()
	}
	params := map[string]string{
		"pid":              "1000",
		"trade_no":         "465397822867521536",
		"out_trade_no":     "ORDER123",
		"channel_order_no": "4200003104202603276324833832",
		"type":             "alipay",
		"name":             "balance recharge",
		"money":            "10",
		"trade_status":     tradeStatusSuccess,
	}
	n, err := e.VerifyNotification(context.Background(), encode(params), nil)
	if err != nil {
		t.Fatalf("notification with channel_order_no must verify: %v", err)
	}
	if n.Status != payment.ProviderStatusSuccess || n.OrderID != "ORDER123" || n.TradeNo != "465397822867521536" {
		t.Fatalf("unexpected notification: %+v", n)
	}

	params["channel_order_no"] = "4200&trade_status=TRADE_SUCCESS"
	if _, err := e.VerifyNotification(context.Background(), encode(params), nil); err == nil {
		t.Fatal("channel_order_no containing &/= must be rejected")
	}
}
