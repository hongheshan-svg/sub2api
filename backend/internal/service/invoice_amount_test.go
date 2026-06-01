//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRound2(t *testing.T) {
	require.Equal(t, 20.00, round2(19.9998))
	require.Equal(t, 60.00, round2(60.0))
	require.Equal(t, 313.33, round2(313.3299))
}

func TestComputeInvoiceAmounts(t *testing.T) {
	// 专票 6%:全额开票,费用为额外项
	fee, invoice := computeInvoiceAmounts(1000.0, 0.06)
	if fee != 60.0 {
		t.Fatalf("fee: want 60.00, got %.2f", fee)
	}
	if invoice != 1000.0 {
		t.Fatalf("invoice: want 1000.00 (full amount), got %.2f", invoice)
	}

	// rate=0:不收费,全额开票
	fee, invoice = computeInvoiceAmounts(1000.0, 0)
	if fee != 0.0 || invoice != 1000.0 {
		t.Fatalf("rate0: want fee=0 invoice=1000, got fee=%.2f invoice=%.2f", fee, invoice)
	}

	// base<=0:零值
	fee, invoice = computeInvoiceAmounts(0, 0.06)
	if fee != 0.0 || invoice != 0.0 {
		t.Fatalf("zero base: want 0/0, got %.2f/%.2f", fee, invoice)
	}
}

func TestResolveInvoiceFeeConfig_DefaultsWhenNoSettingService(t *testing.T) {
	s := &PaymentService{} // invoiceSettingService 为 nil → 回退默认

	rate, category := s.resolveInvoiceFeeConfig(context.Background(), InvoiceTypeVATSpecial)
	require.Equal(t, InvoiceVATSpecialFeeRateDefault, rate)
	require.Equal(t, InvoiceServiceCategoryDefault, category)

	rate, category = s.resolveInvoiceFeeConfig(context.Background(), InvoiceTypeGeneral)
	require.Equal(t, 0.0, rate) // 普票费率恒为 0
	require.Equal(t, InvoiceServiceCategoryDefault, category)
}
