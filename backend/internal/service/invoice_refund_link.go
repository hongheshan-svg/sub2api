package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// OnOrderRefundedForInvoices reconciles invoice requests when a payment order is refunded.
//
// Behaviour by invoice request status:
//   - pending  : detach the order; recompute base/invoice/fee amounts and refund the fee
//     difference to balance; if no orders left, refund the whole fee and hard-delete the request.
//   - completed: flag has_refunded_orders=true; admin must manually void/reissue.
//   - rejected : ignore (the order is already disqualified for invoicing).
//
// Failures are logged but do not abort the refund — invoice bookkeeping is best-effort.
// Caller should pass the canonical orderID after the order has been marked refunded.
func (s *PaymentService) OnOrderRefundedForInvoices(ctx context.Context, orderID int64) {
	if orderID <= 0 {
		return
	}
	db, err := s.invoiceDB()
	if err != nil {
		slog.Warn("invoice refund hook: db unavailable", "order_id", orderID, "error", err)
		return
	}

	rows, err := db.QueryContext(ctx, `
		SELECT ir.id, ir.user_id, ir.serial_no, ir.status
		FROM invoice_request_orders iro
		JOIN invoice_requests ir ON ir.id = iro.invoice_request_id
		WHERE iro.payment_order_id = $1
	`, orderID)
	if err != nil {
		slog.Warn("invoice refund hook: failed to load invoice requests", "order_id", orderID, "error", err)
		return
	}
	type linked struct {
		ReqID    int64
		UserID   int64
		SerialNo string
		Status   string
	}
	var hits []linked
	for rows.Next() {
		var l linked
		if err := rows.Scan(&l.ReqID, &l.UserID, &l.SerialNo, &l.Status); err != nil {
			_ = rows.Close()
			slog.Warn("invoice refund hook: scan failed", "order_id", orderID, "error", err)
			return
		}
		hits = append(hits, l)
	}
	_ = rows.Close()
	if rowsErr := rows.Err(); rowsErr != nil {
		slog.Warn("invoice refund hook: rows error", "order_id", orderID, "error", rowsErr)
		return
	}
	if len(hits) == 0 {
		return
	}

	for _, h := range hits {
		switch h.Status {
		case InvoiceStatusPending:
			s.handleRefundPending(ctx, db, h.ReqID, h.UserID, h.SerialNo, orderID)
		case InvoiceStatusCompleted:
			s.handleRefundCompleted(ctx, db, h.ReqID, h.UserID, h.SerialNo, orderID)
		case InvoiceStatusRejected:
			// rejected: orders already returned to invoiceable pool; nothing to do
		default:
			slog.Warn("invoice refund hook: unexpected status", "request_id", h.ReqID, "status", h.Status)
		}
	}
}

// handleRefundPending detaches the refunded order from a pending request and recomputes its amounts.
// The VAT-special fee was charged on the original amount at submit time, so the difference
// (or the whole fee, when no orders are left and the request is hard-deleted) goes back to balance.
func (s *PaymentService) handleRefundPending(ctx context.Context, db *sql.DB, reqID, userID int64, serialNo string, orderID int64) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		slog.Warn("invoice refund hook: begin tx failed", "request_id", reqID, "error", err)
		return
	}
	defer rollbackIfActive(tx)

	// 锁住申请行并重读状态:调用方读到 pending 之后,管理员可能已完成/驳回,用户可能已取消;
	// 同时与 Cancel/Reject 串行,保证开票费只退一次。
	var status string
	var feeRate, feeAmount float64
	var feeChargedAt, feeRefundedAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `
		SELECT status, fee_rate::float8, fee_amount::float8, fee_charged_at, fee_refunded_at
		FROM invoice_requests
		WHERE id = $1
		FOR UPDATE
	`, reqID).Scan(&status, &feeRate, &feeAmount, &feeChargedAt, &feeRefundedAt); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("invoice refund hook: lock request failed", "request_id", reqID, "error", err)
		}
		return
	}
	if status != InvoiceStatusPending {
		// 先释放行锁,handleRefundCompleted 走另一条连接更新同一行。
		_ = tx.Rollback()
		if status == InvoiceStatusCompleted {
			s.handleRefundCompleted(ctx, db, reqID, userID, serialNo, orderID)
		}
		return
	}

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM invoice_request_orders
		WHERE invoice_request_id = $1 AND payment_order_id = $2
	`, reqID, orderID); err != nil {
		slog.Warn("invoice refund hook: detach failed", "request_id", reqID, "order_id", orderID, "error", err)
		return
	}

	var remaining int
	var remainingAmount float64
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(po.pay_amount), 0)::float8
		FROM invoice_request_orders iro
		JOIN payment_orders po ON po.id = iro.payment_order_id
		WHERE iro.invoice_request_id = $1
	`, reqID).Scan(&remaining, &remainingAmount); err != nil {
		slog.Warn("invoice refund hook: count remaining failed", "request_id", reqID, "error", err)
		return
	}

	// 只有已扣且未退的费用才涉及余额变动;fee_amount 始终表示"当前实际扣着的费用",
	// 之后 Cancel/Reject 按它退款,多次部分退款 + 取消合计正好退回原始费用。
	outstandingFee := 0.0
	if feeChargedAt.Valid && !feeRefundedAt.Valid && feeAmount > 0 {
		outstandingFee = feeAmount
	}

	feeRefund := 0.0
	notifyTitle := ""
	notifyBody := ""

	if remaining == 0 {
		feeRefund = outstandingFee
		if _, err := tx.ExecContext(ctx, `DELETE FROM invoice_requests WHERE id = $1`, reqID); err != nil {
			slog.Warn("invoice refund hook: delete empty request failed", "request_id", reqID, "error", err)
			return
		}
		notifyTitle = "您的开票申请已撤销"
		notifyBody = fmt.Sprintf("申请单号 %s 关联的全部订单已退款，申请已自动撤销。", serialNo)
		if feeRefund > 0 {
			notifyBody += fmt.Sprintf("已扣除的增值税专用发票费 ¥%.2f 已退回余额。", feeRefund)
		}
	} else {
		baseAmount := round2(remainingAmount)
		newFee, invoiceAmount := computeInvoiceAmounts(baseAmount, feeRate)
		if outstandingFee > newFee {
			feeRefund = round2(outstandingFee - newFee)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE invoice_requests
			SET total_amount = $2,
			    base_amount = $2,
			    invoice_amount = $3,
			    fee_amount = $4,
			    has_refunded_orders = true,
			    updated_at = NOW()
			WHERE id = $1
		`, reqID, baseAmount, invoiceAmount, newFee); err != nil {
			slog.Warn("invoice refund hook: recompute amounts failed", "request_id", reqID, "error", err)
			return
		}
		notifyTitle = "申请订单已部分退款"
		notifyBody = fmt.Sprintf("申请单号 %s 中的部分订单已退款，金额已重新计算。", serialNo)
		if feeRefund > 0 {
			notifyBody += fmt.Sprintf("多扣的增值税专用发票费 ¥%.2f 已退回余额。", feeRefund)
		}
	}

	if feeRefund > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET balance = balance + $1 WHERE id = $2`, feeRefund, userID); err != nil {
			slog.Warn("invoice refund hook: refund invoice fee failed", "request_id", reqID, "error", err)
			return
		}
		if err := insertInvoiceFeeLedgerTx(ctx, tx, newInvoiceFeeRefundEntry(userID, feeRefund, serialNo)); err != nil {
			slog.Warn("invoice refund hook: record invoice fee refund ledger failed", "request_id", reqID, "error", err)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		slog.Warn("invoice refund hook: commit failed", "request_id", reqID, "error", err)
		return
	}
	if feeRefund > 0 && s.userService != nil {
		s.userService.InvalidateBalanceCaches(ctx, userID)
	}

	s.writeRefundNotification(ctx, userID, reqID, serialNo, notifyTitle, notifyBody)
}

// handleRefundCompleted flags the completed request so admins can manually void/reissue.
func (s *PaymentService) handleRefundCompleted(ctx context.Context, db *sql.DB, reqID, userID int64, serialNo string, orderID int64) {
	res, err := db.ExecContext(ctx, `
		UPDATE invoice_requests
		SET has_refunded_orders = true,
		    updated_at = NOW()
		WHERE id = $1 AND has_refunded_orders = false
	`, reqID)
	if err != nil {
		slog.Warn("invoice refund hook: flag completed failed", "request_id", reqID, "error", err)
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		// Already flagged from a previous refund — skip duplicate notification.
		return
	}

	s.writeRefundNotification(ctx, userID, reqID,
		serialNo,
		"已开具发票的订单发生退款",
		fmt.Sprintf("申请单号 %s 已开具发票，但其中订单 #%d 发生退款。请联系管理员协助开具红字发票。", serialNo, orderID),
	)
}

// writeRefundNotification persists an in-app notification for the user.
// Best-effort — failures are logged.
func (s *PaymentService) writeRefundNotification(ctx context.Context, userID, reqID int64, serialNo, title, body string) {
	if s == nil || s.invoiceNotifier == nil || userID <= 0 {
		return
	}
	if _, err := s.invoiceNotifier.Create(ctx, CreateUserNotificationInput{
		UserID:   userID,
		Category: NotificationCategoryInvoice,
		Title:    title,
		Body:     body,
		Link:     "/invoice",
		Metadata: map[string]any{"invoice_request_id": reqID, "serial_no": serialNo, "trigger": "refund"},
	}); err != nil {
		var apperr *infraerrors.Error
		if errors.As(err, &apperr) && apperr.Code == 0 {
			return
		}
		slog.Warn("invoice refund hook: notification failed", "user_id", userID, "request_id", reqID, "error", err)
	}
}
