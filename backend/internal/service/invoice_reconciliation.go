package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/lib/pq"
)

// 发票对账：按用户对比「净实付」与「已开票 + 申请中」，让管理员直接看到差额。
//
// 口径：
//   - 实付：曾付款成功的订单 pay_amount 合计（含之后退款的订单）。
//   - 退款：refunded / partially_refunded 订单的已退金额。refund_amount 按到账金额
//     amount 记，这里按 refund_amount/amount 的比例折算回 pay_amount 口径，才能和开票金额比。
//   - 已开票 / 申请中：completed / pending 开票申请的 invoice_amount 合计，驳回的不计。
//   - 超开 = 已开票 + 申请中 − 净实付（>0 时）；未开票 = 净实付 − 已开票 − 申请中（>0 时）。
//   - 异常订单：同一订单挂在多张未驳回申请上（重复开票），或已退款订单仍挂在未驳回申请上
//     （已开票的需红冲）。即使金额相抵，这两类也算异常。
//   - 管理员「对公直发」的发票不关联订单和金额，无法纳入对账。

// Reconciliation scopes for the admin list.
const (
	InvoiceReconciliationScopeAll        = "all"
	InvoiceReconciliationScopeInvoiced   = "invoiced"
	InvoiceReconciliationScopeMismatched = "mismatched"
)

// Per-order reconciliation issues.
const (
	InvoiceReconciliationIssueDuplicate        = "duplicate"
	InvoiceReconciliationIssueRefundedInvoiced = "refunded_invoiced"
)

var invoiceReconciliationPaidStatuses = []string{
	OrderStatusPaid,
	OrderStatusRecharging,
	OrderStatusCompleted,
	OrderStatusRefundRequested,
	OrderStatusRefunding,
	OrderStatusRefundPending,
	OrderStatusPartiallyRefunded,
	OrderStatusRefunded,
	OrderStatusRefundFailed,
}

var invoiceReconciliationRefundedStatuses = []string{
	OrderStatusPartiallyRefunded,
	OrderStatusRefunded,
}

// invoiceReconciliationRefundExpr 把已退金额折算回 pay_amount 口径。$2 = 已退款状态。
const invoiceReconciliationRefundExpr = `CASE WHEN po.status = ANY($2) AND po.amount > 0
		THEN ROUND(po.pay_amount * LEAST(po.refund_amount, po.amount) / po.amount, 2)
		ELSE 0 END`

// invoiceReconciliationCTE 产出每个用户一行的 final 视图。$1 = 已付款状态，$2 = 已退款状态。
const invoiceReconciliationCTE = `
WITH paid AS (
	SELECT po.user_id,
	       SUM(po.pay_amount) AS paid_amount,
	       SUM(` + invoiceReconciliationRefundExpr + `) AS refunded_amount
	FROM payment_orders po
	WHERE po.status = ANY($1)
	GROUP BY po.user_id
),
inv AS (
	SELECT ir.user_id,
	       SUM(CASE WHEN ir.status = 'completed' THEN ir.invoice_amount ELSE 0 END) AS invoiced_amount,
	       SUM(CASE WHEN ir.status = 'pending' THEN ir.invoice_amount ELSE 0 END) AS pending_amount
	FROM invoice_requests ir
	WHERE ir.status IN ('pending', 'completed')
	GROUP BY ir.user_id
),
linked AS (
	SELECT po.user_id,
	       COUNT(*) > 1 AS duplicated,
	       bool_or(po.status = ANY($2)) AS refunded
	FROM invoice_request_orders iro
	JOIN invoice_requests ir ON ir.id = iro.invoice_request_id AND ir.status IN ('pending', 'completed')
	JOIN payment_orders po ON po.id = iro.payment_order_id
	GROUP BY po.user_id, iro.payment_order_id
),
flags AS (
	SELECT user_id,
	       COUNT(*) FILTER (WHERE duplicated) AS duplicate_orders,
	       COUNT(*) FILTER (WHERE refunded) AS refunded_invoiced_orders
	FROM linked
	GROUP BY user_id
),
rec AS (
	SELECT u.id AS user_id,
	       COALESCE(u.username, '') AS username,
	       COALESCE(u.email, '') AS email,
	       COALESCE(p.paid_amount, 0) AS paid_amount,
	       COALESCE(p.refunded_amount, 0) AS refunded_amount,
	       COALESCE(i.invoiced_amount, 0) AS invoiced_amount,
	       COALESCE(i.pending_amount, 0) AS pending_amount,
	       COALESCE(f.duplicate_orders, 0) AS duplicate_orders,
	       COALESCE(f.refunded_invoiced_orders, 0) AS refunded_invoiced_orders,
	       i.user_id IS NOT NULL AS has_invoices
	FROM users u
	LEFT JOIN paid p ON p.user_id = u.id
	LEFT JOIN inv i ON i.user_id = u.id
	LEFT JOIN flags f ON f.user_id = u.id
	WHERE p.user_id IS NOT NULL OR i.user_id IS NOT NULL
),
calc AS (
	SELECT rec.*,
	       paid_amount - refunded_amount AS net_paid_amount,
	       GREATEST(invoiced_amount + pending_amount - (paid_amount - refunded_amount), 0) AS over_invoiced_amount,
	       GREATEST((paid_amount - refunded_amount) - invoiced_amount - pending_amount, 0) AS uninvoiced_amount
	FROM rec
),
final AS (
	SELECT calc.*,
	       (over_invoiced_amount > 0 OR duplicate_orders > 0 OR refunded_invoiced_orders > 0) AS mismatched
	FROM calc
)
`

const invoiceReconciliationRowColumns = `user_id, username, email,
	paid_amount::float8, refunded_amount::float8, net_paid_amount::float8,
	invoiced_amount::float8, pending_amount::float8,
	uninvoiced_amount::float8, over_invoiced_amount::float8,
	duplicate_orders, refunded_invoiced_orders, mismatched`

// InvoiceReconciliationParams filters the admin reconciliation list.
type InvoiceReconciliationParams struct {
	Page     int
	PageSize int
	Keyword  string
	Scope    string
}

// InvoiceReconciliationRow is one user's paid-vs-invoiced comparison.
type InvoiceReconciliationRow struct {
	UserID                 int64   `json:"user_id"`
	Username               string  `json:"username"`
	Email                  string  `json:"email"`
	PaidAmount             float64 `json:"paid_amount"`
	RefundedAmount         float64 `json:"refunded_amount"`
	NetPaidAmount          float64 `json:"net_paid_amount"`
	InvoicedAmount         float64 `json:"invoiced_amount"`
	PendingAmount          float64 `json:"pending_amount"`
	UninvoicedAmount       float64 `json:"uninvoiced_amount"`
	OverInvoicedAmount     float64 `json:"over_invoiced_amount"`
	DuplicateOrders        int     `json:"duplicate_orders"`
	RefundedInvoicedOrders int     `json:"refunded_invoiced_orders"`
	Mismatched             bool    `json:"mismatched"`
}

// InvoiceReconciliationSummary aggregates all users (independent of list filters).
type InvoiceReconciliationSummary struct {
	PaidAmount         float64 `json:"paid_amount"`
	RefundedAmount     float64 `json:"refunded_amount"`
	NetPaidAmount      float64 `json:"net_paid_amount"`
	InvoicedAmount     float64 `json:"invoiced_amount"`
	PendingAmount      float64 `json:"pending_amount"`
	UninvoicedAmount   float64 `json:"uninvoiced_amount"`
	OverInvoicedAmount float64 `json:"over_invoiced_amount"`
	UserCount          int     `json:"user_count"`
	MismatchedUsers    int     `json:"mismatched_users"`
}

// InvoiceReconciliationRequestRef is a non-rejected invoice request an order is attached to.
type InvoiceReconciliationRequestRef struct {
	ID        int64   `json:"id"`
	SerialNo  string  `json:"serial_no"`
	Status    string  `json:"status"`
	InvoiceNo *string `json:"invoice_no,omitempty"`
}

// InvoiceReconciliationOrder is one paid order in a user's reconciliation detail.
type InvoiceReconciliationOrder struct {
	ID             int64                             `json:"id"`
	OutTradeNo     string                            `json:"out_trade_no"`
	PayAmount      float64                           `json:"pay_amount"`
	RefundedAmount float64                           `json:"refunded_amount"`
	Status         string                            `json:"status"`
	OrderType      string                            `json:"order_type"`
	PaymentType    string                            `json:"payment_type"`
	CreatedAt      time.Time                         `json:"created_at"`
	CompletedAt    *time.Time                        `json:"completed_at,omitempty"`
	Requests       []InvoiceReconciliationRequestRef `json:"requests"`
	Issues         []string                          `json:"issues"`
}

// InvoiceReconciliationDetail is a single user's reconciliation with order-level breakdown.
type InvoiceReconciliationDetail struct {
	Summary InvoiceReconciliationRow     `json:"summary"`
	Orders  []InvoiceReconciliationOrder `json:"orders"`
}

func invoiceReconciliationBaseArgs() []any {
	return []any{pq.Array(invoiceReconciliationPaidStatuses), pq.Array(invoiceReconciliationRefundedStatuses)}
}

func scanInvoiceReconciliationRow(scanner interface {
	Scan(dest ...any) error
}) (InvoiceReconciliationRow, error) {
	var row InvoiceReconciliationRow
	err := scanner.Scan(
		&row.UserID, &row.Username, &row.Email,
		&row.PaidAmount, &row.RefundedAmount, &row.NetPaidAmount,
		&row.InvoicedAmount, &row.PendingAmount,
		&row.UninvoicedAmount, &row.OverInvoicedAmount,
		&row.DuplicateOrders, &row.RefundedInvoicedOrders, &row.Mismatched,
	)
	return row, err
}

// ListInvoiceReconciliation returns per-user reconciliation rows, anomalies first.
func (s *PaymentService) ListInvoiceReconciliation(ctx context.Context, params InvoiceReconciliationParams) ([]InvoiceReconciliationRow, int, error) {
	db, err := s.invoiceDB()
	if err != nil {
		return nil, 0, err
	}
	params.Page, params.PageSize = normalizeInvoicePagination(params.Page, params.PageSize)

	args := invoiceReconciliationBaseArgs()
	conds := []string{"TRUE"}
	switch strings.TrimSpace(params.Scope) {
	case "", InvoiceReconciliationScopeAll:
	case InvoiceReconciliationScopeInvoiced:
		conds = append(conds, "has_invoices")
	case InvoiceReconciliationScopeMismatched:
		conds = append(conds, "mismatched")
	default:
		return nil, 0, infraerrors.BadRequest("INVOICE_RECONCILIATION_SCOPE_INVALID", "reconciliation scope is invalid")
	}
	if kw := strings.TrimSpace(params.Keyword); kw != "" {
		args = append(args, "%"+kw+"%", kw)
		conds = append(conds, fmt.Sprintf("(email ILIKE $%d OR username ILIKE $%d OR user_id::text = $%d)", len(args)-1, len(args)-1, len(args)))
	}
	where := " WHERE " + strings.Join(conds, " AND ")

	var total int
	if err := db.QueryRowContext(ctx, invoiceReconciliationCTE+`SELECT COUNT(*) FROM final`+where, args...).Scan(&total); err != nil {
		return nil, 0, infraerrors.InternalServer("INVOICE_RECONCILIATION_FAILED", "failed to count invoice reconciliation").WithCause(err)
	}

	queryArgs := append(append([]any{}, args...), params.PageSize, (params.Page-1)*params.PageSize)
	rows, err := db.QueryContext(ctx, invoiceReconciliationCTE+`
		SELECT `+invoiceReconciliationRowColumns+`
		FROM final`+where+`
		ORDER BY mismatched DESC, over_invoiced_amount DESC, (invoiced_amount + pending_amount) DESC, user_id DESC
		LIMIT $`+strconv.Itoa(len(args)+1)+` OFFSET $`+strconv.Itoa(len(args)+2), queryArgs...)
	if err != nil {
		return nil, 0, infraerrors.InternalServer("INVOICE_RECONCILIATION_FAILED", "failed to list invoice reconciliation").WithCause(err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]InvoiceReconciliationRow, 0)
	for rows.Next() {
		row, err := scanInvoiceReconciliationRow(rows)
		if err != nil {
			return nil, 0, infraerrors.InternalServer("INVOICE_RECONCILIATION_FAILED", "failed to scan invoice reconciliation").WithCause(err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, infraerrors.InternalServer("INVOICE_RECONCILIATION_FAILED", "failed to list invoice reconciliation").WithCause(err)
	}
	return result, total, nil
}

// GetInvoiceReconciliationSummary returns platform-wide totals across all users.
func (s *PaymentService) GetInvoiceReconciliationSummary(ctx context.Context) (*InvoiceReconciliationSummary, error) {
	db, err := s.invoiceDB()
	if err != nil {
		return nil, err
	}
	var sum InvoiceReconciliationSummary
	if err := db.QueryRowContext(ctx, invoiceReconciliationCTE+`
		SELECT COALESCE(SUM(paid_amount), 0)::float8,
		       COALESCE(SUM(refunded_amount), 0)::float8,
		       COALESCE(SUM(net_paid_amount), 0)::float8,
		       COALESCE(SUM(invoiced_amount), 0)::float8,
		       COALESCE(SUM(pending_amount), 0)::float8,
		       COALESCE(SUM(uninvoiced_amount), 0)::float8,
		       COALESCE(SUM(over_invoiced_amount), 0)::float8,
		       COUNT(*),
		       COUNT(*) FILTER (WHERE mismatched)
		FROM final
	`, invoiceReconciliationBaseArgs()...).Scan(
		&sum.PaidAmount, &sum.RefundedAmount, &sum.NetPaidAmount,
		&sum.InvoicedAmount, &sum.PendingAmount,
		&sum.UninvoicedAmount, &sum.OverInvoicedAmount,
		&sum.UserCount, &sum.MismatchedUsers,
	); err != nil {
		return nil, infraerrors.InternalServer("INVOICE_RECONCILIATION_FAILED", "failed to summarize invoice reconciliation").WithCause(err)
	}
	return &sum, nil
}

// GetUserInvoiceReconciliation returns one user's reconciliation with each paid order
// and the non-rejected invoice requests it is attached to.
func (s *PaymentService) GetUserInvoiceReconciliation(ctx context.Context, userID int64) (*InvoiceReconciliationDetail, error) {
	db, err := s.invoiceDB()
	if err != nil {
		return nil, err
	}
	args := append(invoiceReconciliationBaseArgs(), userID)

	summary, err := scanInvoiceReconciliationRow(db.QueryRowContext(ctx, invoiceReconciliationCTE+`
		SELECT `+invoiceReconciliationRowColumns+`
		FROM final
		WHERE user_id = $3
	`, args...))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, infraerrors.NotFound("INVOICE_RECONCILIATION_USER_NOT_FOUND", "user has no paid orders or invoice requests")
		}
		return nil, infraerrors.InternalServer("INVOICE_RECONCILIATION_FAILED", "failed to load user invoice reconciliation").WithCause(err)
	}

	refsByOrder, err := queryInvoiceReconciliationRequestRefs(ctx, db, userID)
	if err != nil {
		return nil, err
	}

	rows, err := db.QueryContext(ctx, `
		SELECT po.id, po.out_trade_no, po.pay_amount::float8, (`+invoiceReconciliationRefundExpr+`)::float8,
		       po.status, po.order_type, po.payment_type, po.created_at, po.completed_at
		FROM payment_orders po
		WHERE po.status = ANY($1) AND po.user_id = $3
		ORDER BY po.created_at DESC, po.id DESC
	`, args...)
	if err != nil {
		return nil, infraerrors.InternalServer("INVOICE_RECONCILIATION_FAILED", "failed to load user orders").WithCause(err)
	}
	defer func() { _ = rows.Close() }()

	refunded := make(map[string]bool, len(invoiceReconciliationRefundedStatuses))
	for _, st := range invoiceReconciliationRefundedStatuses {
		refunded[st] = true
	}
	orders := make([]InvoiceReconciliationOrder, 0)
	for rows.Next() {
		var o InvoiceReconciliationOrder
		if err := rows.Scan(&o.ID, &o.OutTradeNo, &o.PayAmount, &o.RefundedAmount,
			&o.Status, &o.OrderType, &o.PaymentType, &o.CreatedAt, &o.CompletedAt); err != nil {
			return nil, infraerrors.InternalServer("INVOICE_RECONCILIATION_FAILED", "failed to scan user order").WithCause(err)
		}
		o.Requests = refsByOrder[o.ID]
		if o.Requests == nil {
			o.Requests = []InvoiceReconciliationRequestRef{}
		}
		o.Issues = []string{}
		if len(o.Requests) > 1 {
			o.Issues = append(o.Issues, InvoiceReconciliationIssueDuplicate)
		}
		if len(o.Requests) > 0 && refunded[o.Status] {
			o.Issues = append(o.Issues, InvoiceReconciliationIssueRefundedInvoiced)
		}
		orders = append(orders, o)
	}
	if err := rows.Err(); err != nil {
		return nil, infraerrors.InternalServer("INVOICE_RECONCILIATION_FAILED", "failed to load user orders").WithCause(err)
	}
	return &InvoiceReconciliationDetail{Summary: summary, Orders: orders}, nil
}

func queryInvoiceReconciliationRequestRefs(ctx context.Context, db *sql.DB, userID int64) (map[int64][]InvoiceReconciliationRequestRef, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT iro.payment_order_id, ir.id, ir.serial_no, ir.status, ir.invoice_no
		FROM invoice_request_orders iro
		JOIN invoice_requests ir ON ir.id = iro.invoice_request_id
		WHERE ir.user_id = $1 AND ir.status IN ('pending', 'completed')
		ORDER BY ir.created_at, ir.id
	`, userID)
	if err != nil {
		return nil, infraerrors.InternalServer("INVOICE_RECONCILIATION_FAILED", "failed to load invoice links").WithCause(err)
	}
	defer func() { _ = rows.Close() }()

	refs := make(map[int64][]InvoiceReconciliationRequestRef)
	for rows.Next() {
		var orderID int64
		var ref InvoiceReconciliationRequestRef
		if err := rows.Scan(&orderID, &ref.ID, &ref.SerialNo, &ref.Status, &ref.InvoiceNo); err != nil {
			return nil, infraerrors.InternalServer("INVOICE_RECONCILIATION_FAILED", "failed to scan invoice link").WithCause(err)
		}
		refs[orderID] = append(refs[orderID], ref)
	}
	if err := rows.Err(); err != nil {
		return nil, infraerrors.InternalServer("INVOICE_RECONCILIATION_FAILED", "failed to load invoice links").WithCause(err)
	}
	return refs, nil
}
