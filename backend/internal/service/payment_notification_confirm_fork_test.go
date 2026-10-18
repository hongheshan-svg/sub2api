//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/provider"
	"github.com/stretchr/testify/require"
)

// fork: 关闭查单确认的易支付实例（支付FM 等没有 api.php?act=order 的平台）
// 直接放行验签通过的回调，不访问上游、不写 PAYMENT_UPSTREAM_UNCONFIRMED。
func TestConfirmNotificationUpstreamEasyPayConfirmDisabled(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("confirm-disabled@example.com").
		SetPasswordHash("hash").
		SetUsername("confirm-disabled-user").
		Save(ctx)
	require.NoError(t, err)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(10).
		SetPayAmount(10).
		SetFeeRate(0).
		SetRechargeCode("PAY-CONFIRM-DISABLED").
		SetOutTradeNo("sub2_fm_order").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	unconfirmedAudits := func() int {
		count, err := client.PaymentAuditLog.Query().
			Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("PAYMENT_UPSTREAM_UNCONFIRMED")).
			Count(ctx)
		require.NoError(t, err)
		return count
	}

	var queried atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		queried.Store(true)
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(upstream.Close)

	config := map[string]string{
		"pid":       "4508888888888",
		"pkey":      "pkey-1",
		"apiBase":   upstream.URL,
		"notifyUrl": "https://example.com/api/v1/payment/webhook/easypay",
		"returnUrl": "https://example.com/payment/result",
	}
	svc := &PaymentService{entClient: client}
	n := func() *payment.PaymentNotification {
		return &payment.PaymentNotification{OrderID: order.OutTradeNo, TradeNo: "465397822867521536", Amount: 10, Status: payment.NotificationStatusSuccess}
	}

	config["notifyConfirm"] = "off"
	disabled, err := provider.NewEasyPay("fm", config)
	require.NoError(t, err)
	require.NoError(t, svc.ConfirmNotificationUpstream(ctx, disabled, n()))
	require.False(t, queried.Load(), "disabled instance must not query upstream")
	require.Zero(t, unconfirmedAudits())

	// 同样的平台若保持开启，确认失败、拒绝入账。
	config["notifyConfirm"] = "query"
	enabled, err := provider.NewEasyPay("fm", config)
	require.NoError(t, err)
	require.Error(t, svc.ConfirmNotificationUpstream(ctx, enabled, n()))
	require.True(t, queried.Load())
	require.Equal(t, 1, unconfirmedAudits())
}
