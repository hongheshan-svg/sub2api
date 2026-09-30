//go:build integration

package repository

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// createPaidOrderForReconciliation 创建一笔已付款订单，amount(到账) 与 pay_amount(实付) 可不同，
// 用来验证退款金额按 refund_amount/amount 比例折算回实付口径。
func createPaidOrderForReconciliation(t *testing.T, ctx context.Context, userID int64, amount, payAmount float64, status string, refundAmount float64) int64 {
	t.Helper()
	suffix := invoiceFixtureSuffix("recon-order")
	now := time.Now()
	create := integrationEntClient.PaymentOrder.Create().
		SetUserID(userID).
		SetUserEmail(suffix + "@invoice.test").
		SetUserName(suffix).
		SetAmount(amount).
		SetPayAmount(payAmount).
		SetFeeRate(0).
		SetRechargeCode("REC-" + suffix).
		SetOutTradeNo("sub2_" + suffix).
		SetPaymentType(payment.TypeStripe).
		SetPaymentTradeNo("pi_" + suffix).
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(status).
		SetExpiresAt(now.Add(time.Hour)).
		SetPaidAt(now).
		SetCompletedAt(now).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com")
	if refundAmount > 0 {
		create = create.SetRefundAmount(refundAmount).SetRefundAt(now)
	}
	o, err := create.Save(ctx)
	require.NoError(t, err, "create paid order")
	return o.ID
}

func submitInvoiceForReconciliation(t *testing.T, ctx context.Context, svc *service.PaymentService, fx invoiceFixture, orderIDs ...int64) int64 {
	t.Helper()
	req, err := svc.CreateInvoiceRequest(ctx, fx.userID, service.CreateInvoiceRequestInput{ProfileID: fx.profileID, OrderIDs: orderIDs})
	require.NoError(t, err)
	return req.ID
}

func completeInvoiceForReconciliation(t *testing.T, ctx context.Context, reqID int64) {
	t.Helper()
	_, err := integrationDB.ExecContext(ctx, `
		UPDATE invoice_requests SET status = 'completed', invoice_no = $2, completed_at = NOW() WHERE id = $1
	`, reqID, "INV-REC-"+strconv.FormatInt(reqID, 10))
	require.NoError(t, err)
}

func TestInvoiceReconciliationComparesPaidAndInvoiced(t *testing.T) {
	ctx := context.Background()
	svc := newInvoiceIntegrationService()

	before, err := svc.GetInvoiceReconciliationSummary(ctx)
	require.NoError(t, err)

	token := invoiceFixtureSuffix("recon")

	// 用户 A：各种异常都有。
	a := createInvoiceFixture(t, ctx, token+"-a", 100)
	a1 := createInvoiceableOrder(t, ctx, a.userID, 100) // 已开票
	a2 := createInvoiceableOrder(t, ctx, a.userID, 50)  // 申请中
	a3 := createInvoiceableOrder(t, ctx, a.userID, 30)  // 未开票
	a4 := createInvoiceableOrder(t, ctx, a.userID, 40)  // 已开票后全额退款 → 需红冲
	a5 := createInvoiceableOrder(t, ctx, a.userID, 20)  // 挂在两张申请上 → 重复开票
	completeInvoiceForReconciliation(t, ctx, submitInvoiceForReconciliation(t, ctx, svc, a, a1))
	submitInvoiceForReconciliation(t, ctx, svc, a, a2)
	completeInvoiceForReconciliation(t, ctx, submitInvoiceForReconciliation(t, ctx, svc, a, a4))
	markInvoiceOrderRefunded(t, ctx, a4, 40)
	submitInvoiceForReconciliation(t, ctx, svc, a, a5)
	// 模拟修复前遗留的重复申请：同一订单再挂一张 pending 申请（现在的代码已无法产生）。
	var dupReqID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO invoice_requests (user_id, profile_id, serial_no, status, profile_snapshot, total_amount, base_amount, invoice_amount)
		VALUES ($1, $2, $3, 'pending', '{}'::jsonb, 20, 20, 20)
		RETURNING id
	`, a.userID, a.profileID, "INVDUP"+strconv.FormatInt(time.Now().UnixNano(), 36)).Scan(&dupReqID))
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO invoice_request_orders (invoice_request_id, payment_order_id) VALUES ($1, $2)`, dupReqID, a5)
	require.NoError(t, err)

	// 用户 B：正常，部分已开票。
	b := createInvoiceFixture(t, ctx, token+"-b", 100)
	b1 := createInvoiceableOrder(t, ctx, b.userID, 80)
	createInvoiceableOrder(t, ctx, b.userID, 20)
	completeInvoiceForReconciliation(t, ctx, submitInvoiceForReconciliation(t, ctx, svc, b, b1))

	// 用户 C：只付款未开票；部分退款且到账 100、实付 110，退 50 → 折算实付口径退 55。
	c := createInvoiceFixture(t, ctx, token+"-c", 0)
	createPaidOrderForReconciliation(t, ctx, c.userID, 100, 110, service.OrderStatusPartiallyRefunded, 50)
	// 未付款的订单不计入实付。
	createPaidOrderForReconciliation(t, ctx, c.userID, 999, 999, service.OrderStatusPending, 0)

	rows, total, err := svc.ListInvoiceReconciliation(ctx, service.InvoiceReconciliationParams{Page: 1, PageSize: 20, Keyword: token})
	require.NoError(t, err)
	require.Equal(t, 3, total)
	require.Len(t, rows, 3)
	// 异常用户排最前，其余按已开票 + 申请中降序。
	require.Equal(t, []int64{a.userID, b.userID, c.userID}, []int64{rows[0].UserID, rows[1].UserID, rows[2].UserID})

	ra := rows[0]
	require.InDelta(t, 240.0, ra.PaidAmount, 1e-6)
	require.InDelta(t, 40.0, ra.RefundedAmount, 1e-6)
	require.InDelta(t, 200.0, ra.NetPaidAmount, 1e-6)
	require.InDelta(t, 140.0, ra.InvoicedAmount, 1e-6) // a1 100 + a4 40
	require.InDelta(t, 90.0, ra.PendingAmount, 1e-6)   // a2 50 + a5 20 + 重复的 20
	require.InDelta(t, 0.0, ra.UninvoicedAmount, 1e-6)
	require.InDelta(t, 30.0, ra.OverInvoicedAmount, 1e-6) // 230 − 200
	require.Equal(t, 1, ra.DuplicateOrders)
	require.Equal(t, 1, ra.RefundedInvoicedOrders)
	require.True(t, ra.Mismatched)

	rb := rows[1]
	require.InDelta(t, 100.0, rb.NetPaidAmount, 1e-6)
	require.InDelta(t, 80.0, rb.InvoicedAmount, 1e-6)
	require.InDelta(t, 20.0, rb.UninvoicedAmount, 1e-6)
	require.InDelta(t, 0.0, rb.OverInvoicedAmount, 1e-6)
	require.False(t, rb.Mismatched)

	rc := rows[2]
	require.InDelta(t, 110.0, rc.PaidAmount, 1e-6)
	require.InDelta(t, 55.0, rc.RefundedAmount, 1e-6)
	require.InDelta(t, 55.0, rc.NetPaidAmount, 1e-6)
	require.InDelta(t, 55.0, rc.UninvoicedAmount, 1e-6)
	require.False(t, rc.Mismatched)

	scopeUsers := func(scope string) []int64 {
		rows, _, err := svc.ListInvoiceReconciliation(ctx, service.InvoiceReconciliationParams{Page: 1, PageSize: 20, Keyword: token, Scope: scope})
		require.NoError(t, err)
		ids := make([]int64, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.UserID)
		}
		return ids
	}
	require.Equal(t, []int64{a.userID, b.userID}, scopeUsers(service.InvoiceReconciliationScopeInvoiced))
	require.Equal(t, []int64{a.userID}, scopeUsers(service.InvoiceReconciliationScopeMismatched))
	_, _, err = svc.ListInvoiceReconciliation(ctx, service.InvoiceReconciliationParams{Scope: "bogus"})
	require.Equal(t, "INVOICE_RECONCILIATION_SCOPE_INVALID", infraerrors.Reason(err))

	// 汇总按全平台统计：用增量断言，避免依赖共享库里的其他数据。
	after, err := svc.GetInvoiceReconciliationSummary(ctx)
	require.NoError(t, err)
	require.InDelta(t, 240.0+100+110, after.PaidAmount-before.PaidAmount, 1e-6)
	require.InDelta(t, 40.0+55, after.RefundedAmount-before.RefundedAmount, 1e-6)
	require.InDelta(t, 140.0+80, after.InvoicedAmount-before.InvoicedAmount, 1e-6)
	require.InDelta(t, 90.0, after.PendingAmount-before.PendingAmount, 1e-6)
	require.InDelta(t, 20.0+55, after.UninvoicedAmount-before.UninvoicedAmount, 1e-6)
	require.InDelta(t, 30.0, after.OverInvoicedAmount-before.OverInvoicedAmount, 1e-6)
	require.Equal(t, 3, after.UserCount-before.UserCount)
	require.Equal(t, 1, after.MismatchedUsers-before.MismatchedUsers)

	detail, err := svc.GetUserInvoiceReconciliation(ctx, a.userID)
	require.NoError(t, err)
	require.Equal(t, ra, detail.Summary)
	require.Len(t, detail.Orders, 5)
	byID := make(map[int64]service.InvoiceReconciliationOrder, len(detail.Orders))
	for _, o := range detail.Orders {
		byID[o.ID] = o
	}
	require.Len(t, byID[a1].Requests, 1)
	require.Equal(t, "completed", byID[a1].Requests[0].Status)
	require.Empty(t, byID[a1].Issues)
	require.Empty(t, byID[a3].Requests)
	require.Empty(t, byID[a3].Issues)
	require.Equal(t, []string{service.InvoiceReconciliationIssueRefundedInvoiced}, byID[a4].Issues)
	require.InDelta(t, 40.0, byID[a4].RefundedAmount, 1e-6)
	require.Len(t, byID[a5].Requests, 2)
	require.Equal(t, []string{service.InvoiceReconciliationIssueDuplicate}, byID[a5].Issues)

	_, err = svc.GetUserInvoiceReconciliation(ctx, -1)
	require.Equal(t, "INVOICE_RECONCILIATION_USER_NOT_FOUND", infraerrors.Reason(err))
}
