package service

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestInvoiceFeeLedgerEntries(t *testing.T) {
	charge := newInvoiceFeeChargeEntry(42, 6.0, "INV-1")
	if charge.UserID != 42 || charge.Value != -6.0 {
		t.Fatalf("charge: got userID=%d value=%.2f, want 42/-6.00", charge.UserID, charge.Value)
	}
	if !strings.Contains(charge.Note, "INV-1") || !strings.Contains(charge.Note, "发票服务费") {
		t.Fatalf("charge note missing serial/label: %q", charge.Note)
	}

	refund := newInvoiceFeeRefundEntry(42, 6.0, "INV-1")
	if refund.UserID != 42 || refund.Value != 6.0 {
		t.Fatalf("refund: got userID=%d value=%.2f, want 42/6.00", refund.UserID, refund.Value)
	}
	if !strings.Contains(refund.Note, "退回") || !strings.Contains(refund.Note, "INV-1") {
		t.Fatalf("refund note missing label/serial: %q", refund.Note)
	}
}

func TestInsertInvoiceFeeLedgerTx(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO redeem_codes").
		WithArgs(sqlmock.AnyArg(), RedeemTypeInvoiceFee, -6.0, int64(42), "发票服务费 · 申请 INV-1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := insertInvoiceFeeLedgerTx(ctx, tx, newInvoiceFeeChargeEntry(42, 6.0, "INV-1")); err != nil {
		t.Fatalf("insertInvoiceFeeLedgerTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
