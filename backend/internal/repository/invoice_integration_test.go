//go:build integration

package repository

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 开票申请的防重复提交依赖 Postgres 行锁 + READ COMMITTED 语义，退款联动依赖
// Postgres 方言 SQL（FOR UPDATE、::float8），都只能在真实库上验证。

var invoiceFixtureSeq atomic.Int64

type invoiceFixture struct {
	userID    int64
	profileID int64
}

func newInvoiceIntegrationService() *service.PaymentService {
	return service.NewPaymentService(integrationEntClient, nil, nil, nil, nil, nil, nil, nil, nil)
}

func invoiceFixtureSuffix(name string) string {
	return name + "-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.FormatInt(invoiceFixtureSeq.Add(1), 10)
}

func createInvoiceFixture(t *testing.T, ctx context.Context, name string, balance float64) invoiceFixture {
	t.Helper()
	email := invoiceFixtureSuffix(name) + "@invoice.test"
	u, err := integrationEntClient.User.Create().
		SetEmail(email).
		SetPasswordHash("hash").
		SetStatus(service.StatusActive).
		SetRole(service.RoleUser).
		SetBalance(balance).
		Save(ctx)
	require.NoError(t, err, "create user")
	// 被测代码自己管理事务，数据是真实提交的；共享库里其他 suite（如 RedeemCodeRepoSuite）
	// 按整表断言，所以必须清干净。redeem_codes.used_by 是 ON DELETE SET NULL，要显式删。
	t.Cleanup(func() {
		for _, stmt := range []string{
			`DELETE FROM redeem_codes WHERE used_by = $1`,
			`DELETE FROM invoice_requests WHERE user_id = $1`,
			`DELETE FROM payment_orders WHERE user_id = $1`,
			`DELETE FROM invoice_profiles WHERE user_id = $1`,
			`DELETE FROM users WHERE id = $1`,
		} {
			_, err := integrationDB.ExecContext(context.Background(), stmt, u.ID)
			require.NoError(t, err, stmt)
		}
	})

	var profileID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO invoice_profiles (user_id, title, tax_number, email, invoice_type)
		VALUES ($1, '测试科技有限公司', '91110000000000000X', $2, 'vat_special')
		RETURNING id
	`, u.ID, email).Scan(&profileID), "create invoice profile")
	return invoiceFixture{userID: u.ID, profileID: profileID}
}

func createInvoiceableOrder(t *testing.T, ctx context.Context, userID int64, payAmount float64) int64 {
	t.Helper()
	suffix := invoiceFixtureSuffix("order")
	now := time.Now()
	o, err := integrationEntClient.PaymentOrder.Create().
		SetUserID(userID).
		SetUserEmail(suffix + "@invoice.test").
		SetUserName(suffix).
		SetAmount(payAmount).
		SetPayAmount(payAmount).
		SetFeeRate(0).
		SetRechargeCode("INV-" + suffix).
		SetOutTradeNo("sub2_" + suffix).
		SetPaymentType(payment.TypeStripe).
		SetPaymentTradeNo("pi_" + suffix).
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(service.OrderStatusCompleted).
		SetExpiresAt(now.Add(time.Hour)).
		SetPaidAt(now).
		SetCompletedAt(now).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err, "create payment order")
	return o.ID
}

func markInvoiceOrderRefunded(t *testing.T, ctx context.Context, orderID int64, amount float64) {
	t.Helper()
	require.NoError(t, integrationEntClient.PaymentOrder.UpdateOneID(orderID).
		SetStatus(service.OrderStatusRefunded).
		SetRefundAmount(amount).
		SetRefundAt(time.Now()).
		Exec(ctx), "mark order refunded")
}

func invoiceUserBalance(t *testing.T, ctx context.Context, userID int64) float64 {
	t.Helper()
	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance::float8 FROM users WHERE id = $1`, userID).Scan(&balance))
	return balance
}

func invoiceFeeLedgerValues(t *testing.T, ctx context.Context, userID int64) []float64 {
	t.Helper()
	rows, err := integrationDB.QueryContext(ctx, `
		SELECT value::float8 FROM redeem_codes
		WHERE used_by = $1 AND type = $2
		ORDER BY id
	`, userID, service.RedeemTypeInvoiceFee)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	values := make([]float64, 0)
	for rows.Next() {
		var v float64
		require.NoError(t, rows.Scan(&v))
		values = append(values, v)
	}
	require.NoError(t, rows.Err())
	return values
}

func TestInvoiceCreateRequestConcurrentSubmitsInvoiceOrderOnce(t *testing.T) {
	ctx := context.Background()
	svc := newInvoiceIntegrationService()
	fx := createInvoiceFixture(t, ctx, "invoice-concurrent", 100)
	orderID := createInvoiceableOrder(t, ctx, fx.userID, 50)

	const workers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = svc.CreateInvoiceRequest(ctx, fx.userID, service.CreateInvoiceRequestInput{
				ProfileID: fx.profileID,
				OrderIDs:  []int64{orderID},
			})
		}(i)
	}
	close(start)
	wg.Wait()

	succeeded := 0
	for _, err := range errs {
		if err == nil {
			succeeded++
			continue
		}
		require.Equal(t, "INVOICE_ORDER_ALREADY_REQUESTED", infraerrors.Reason(err), "unexpected error: %v", err)
	}
	require.Equal(t, 1, succeeded, "exactly one concurrent submit may succeed")

	var links int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM invoice_request_orders WHERE payment_order_id = $1`, orderID).Scan(&links))
	require.Equal(t, 1, links, "order must be attached to a single invoice request")
	// 50 × 6% = 3.00 的专票费只能扣一次。
	require.InDelta(t, 97.0, invoiceUserBalance(t, ctx, fx.userID), 1e-6)
	require.InDeltaSlice(t, []float64{-3.0}, invoiceFeeLedgerValues(t, ctx, fx.userID), 1e-6)
}

func TestInvoiceRefundHookPendingPartialRefundRecomputesAmountsAndFee(t *testing.T) {
	ctx := context.Background()
	svc := newInvoiceIntegrationService()
	fx := createInvoiceFixture(t, ctx, "invoice-partial-refund", 100)
	keepID := createInvoiceableOrder(t, ctx, fx.userID, 60)
	refundedID := createInvoiceableOrder(t, ctx, fx.userID, 40)

	req, err := svc.CreateInvoiceRequest(ctx, fx.userID, service.CreateInvoiceRequestInput{
		ProfileID: fx.profileID,
		OrderIDs:  []int64{keepID, refundedID},
	})
	require.NoError(t, err)
	require.InDelta(t, 100.0, req.InvoiceAmount, 1e-6)
	require.InDelta(t, 6.0, req.FeeAmount, 1e-6)
	require.InDelta(t, 94.0, invoiceUserBalance(t, ctx, fx.userID), 1e-6)

	markInvoiceOrderRefunded(t, ctx, refundedID, 40)
	svc.OnOrderRefundedForInvoices(ctx, refundedID)

	got, err := svc.GetInvoiceRequest(ctx, fx.userID, req.ID)
	require.NoError(t, err)
	require.Equal(t, service.InvoiceStatusPending, got.Status)
	require.True(t, got.HasRefundedOrders)
	require.Len(t, got.Orders, 1)
	require.Equal(t, keepID, got.Orders[0].ID)
	// 管理员后台按 invoice_amount 开票，退款后必须同步降下来，不能再按 100 开。
	require.InDelta(t, 60.0, got.TotalAmount, 1e-6)
	require.InDelta(t, 60.0, got.BaseAmount, 1e-6)
	require.InDelta(t, 60.0, got.InvoiceAmount, 1e-6)
	require.InDelta(t, 3.6, got.FeeAmount, 1e-6)
	// 多扣的 6.00 - 3.60 = 2.40 退回余额。
	require.InDelta(t, 96.4, invoiceUserBalance(t, ctx, fx.userID), 1e-6)

	// 之后用户取消：按当前 fee_amount 退剩余费用，合计正好退回原始 6.00。
	require.NoError(t, svc.CancelInvoiceRequest(ctx, fx.userID, req.ID))
	require.InDelta(t, 100.0, invoiceUserBalance(t, ctx, fx.userID), 1e-6)
	require.InDeltaSlice(t, []float64{-6.0, 2.4, 3.6}, invoiceFeeLedgerValues(t, ctx, fx.userID), 1e-6)
}

func TestInvoiceRefundHookPendingAllRefundedDeletesRequestAndRefundsFee(t *testing.T) {
	ctx := context.Background()
	svc := newInvoiceIntegrationService()
	fx := createInvoiceFixture(t, ctx, "invoice-full-refund", 100)
	orderID := createInvoiceableOrder(t, ctx, fx.userID, 50)

	req, err := svc.CreateInvoiceRequest(ctx, fx.userID, service.CreateInvoiceRequestInput{
		ProfileID: fx.profileID,
		OrderIDs:  []int64{orderID},
	})
	require.NoError(t, err)
	require.InDelta(t, 97.0, invoiceUserBalance(t, ctx, fx.userID), 1e-6)

	markInvoiceOrderRefunded(t, ctx, orderID, 50)
	svc.OnOrderRefundedForInvoices(ctx, orderID)

	_, err = svc.GetInvoiceRequest(ctx, fx.userID, req.ID)
	require.Equal(t, "INVOICE_REQUEST_NOT_FOUND", infraerrors.Reason(err))
	require.InDelta(t, 100.0, invoiceUserBalance(t, ctx, fx.userID), 1e-6)
	require.InDeltaSlice(t, []float64{-3.0, 3.0}, invoiceFeeLedgerValues(t, ctx, fx.userID), 1e-6)
}

func TestInvoiceRefundHookCompletedRequestOnlyFlagsRefund(t *testing.T) {
	ctx := context.Background()
	svc := newInvoiceIntegrationService()
	fx := createInvoiceFixture(t, ctx, "invoice-completed-refund", 100)
	orderID := createInvoiceableOrder(t, ctx, fx.userID, 50)

	req, err := svc.CreateInvoiceRequest(ctx, fx.userID, service.CreateInvoiceRequestInput{
		ProfileID: fx.profileID,
		OrderIDs:  []int64{orderID},
	})
	require.NoError(t, err)
	// 模拟管理员已开票（CompleteInvoiceRequest 需要发邮件，这里直接改状态）。
	_, err = integrationDB.ExecContext(ctx, `
		UPDATE invoice_requests SET status = $2, invoice_no = 'INV-TEST-0001', completed_at = NOW() WHERE id = $1
	`, req.ID, service.InvoiceStatusCompleted)
	require.NoError(t, err)

	markInvoiceOrderRefunded(t, ctx, orderID, 50)
	svc.OnOrderRefundedForInvoices(ctx, orderID)

	// 发票已开出：只打标等管理员红冲，金额与已收的开票费都不动。
	got, err := svc.GetInvoiceRequest(ctx, fx.userID, req.ID)
	require.NoError(t, err)
	require.Equal(t, service.InvoiceStatusCompleted, got.Status)
	require.True(t, got.HasRefundedOrders)
	require.Len(t, got.Orders, 1)
	require.InDelta(t, 50.0, got.InvoiceAmount, 1e-6)
	require.InDelta(t, 3.0, got.FeeAmount, 1e-6)
	require.InDelta(t, 97.0, invoiceUserBalance(t, ctx, fx.userID), 1e-6)
}
