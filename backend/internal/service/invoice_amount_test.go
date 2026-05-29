//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRound2(t *testing.T) {
	require.Equal(t, 20.00, round2(19.9998))
	require.Equal(t, 60.00, round2(60.0))
	require.Equal(t, 313.33, round2(313.3299))
}

func TestComputeInvoiceAmounts(t *testing.T) {
	// 专票 6%
	fee, inv := computeInvoiceAmounts(1000.0, 0.06)
	require.Equal(t, 60.00, fee)
	require.Equal(t, 940.00, inv)

	// 普票(费率 0)
	fee, inv = computeInvoiceAmounts(1000.0, 0)
	require.Equal(t, 0.00, fee)
	require.Equal(t, 1000.00, inv)

	// 舍入自洽:fee+invoice 必须等于 base
	fee, inv = computeInvoiceAmounts(333.33, 0.06)
	require.Equal(t, 20.00, fee)
	require.Equal(t, 313.33, inv)
	require.Equal(t, 333.33, round2(fee+inv))

	// 零金额
	fee, inv = computeInvoiceAmounts(0, 0.06)
	require.Equal(t, 0.00, fee)
	require.Equal(t, 0.00, inv)
}
