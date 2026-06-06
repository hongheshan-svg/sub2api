package service

import (
	"context"
	"database/sql"
	"fmt"
)

// invoiceFeeLedgerEntry is one invoice-fee balance-ledger row.
// Value<0 = charge (deduction at submit); Value>0 = refund (reject/cancel).
type invoiceFeeLedgerEntry struct {
	UserID int64
	Value  float64
	Note   string
}

// newInvoiceFeeChargeEntry builds the ledger entry for charging the invoice fee.
func newInvoiceFeeChargeEntry(userID int64, feeAmount float64, serialNo string) invoiceFeeLedgerEntry {
	return invoiceFeeLedgerEntry{
		UserID: userID,
		Value:  -feeAmount,
		Note:   fmt.Sprintf("发票服务费 · 申请 %s", serialNo),
	}
}

// newInvoiceFeeRefundEntry builds the ledger entry for refunding the invoice fee.
func newInvoiceFeeRefundEntry(userID int64, feeAmount float64, serialNo string) invoiceFeeLedgerEntry {
	return invoiceFeeLedgerEntry{
		UserID: userID,
		Value:  feeAmount,
		Note:   fmt.Sprintf("发票服务费退回 · 申请 %s", serialNo),
	}
}

// insertInvoiceFeeLedgerTx writes one invoice-fee ledger row into redeem_codes
// within the caller's transaction, mirroring the admin_balance adjustment record
// created by adminServiceImpl.UpdateUserBalance. The synthetic code is generated
// the same way; status is 'used' so it can never be redeemed as a code.
func insertInvoiceFeeLedgerTx(ctx context.Context, tx *sql.Tx, e invoiceFeeLedgerEntry) error {
	code, err := GenerateRedeemCode()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO redeem_codes (code, type, value, status, used_by, used_at, notes)
		VALUES ($1, $2, $3, 'used', $4, NOW(), $5)
	`, code, RedeemTypeInvoiceFee, e.Value, e.UserID, e.Note)
	return err
}
