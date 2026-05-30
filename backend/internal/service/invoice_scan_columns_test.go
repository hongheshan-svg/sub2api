//go:build unit

package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// destCountScanner records how many destinations a scan function passes to
// Scan, then returns a sentinel error so the scan function short-circuits
// before touching any (nil) values.  This lets us assert, without a database,
// that each scanner's destination list stays in lock-step with the canonical
// invoiceRequestColumns SELECT list.
type destCountScanner struct{ n int }

var errStopScan = errors.New("stop scan after counting destinations")

func (s *destCountScanner) Scan(dest ...any) error {
	s.n = len(dest)
	return errStopScan
}

func invoiceColumnCount() int {
	return len(strings.Split(invoiceRequestColumns, ","))
}

// TestScanInvoiceRequest_DestCountMatchesColumns guards the user-facing scanner.
func TestScanInvoiceRequest_DestCountMatchesColumns(t *testing.T) {
	s := &destCountScanner{}
	_, err := scanInvoiceRequest(s)
	require.ErrorIs(t, err, errStopScan)
	require.Equal(t, invoiceColumnCount(), s.n,
		"scanInvoiceRequest destinations must match invoiceRequestColumns count")
}

// TestScanAdminInvoiceRequest_DestCountMatchesColumns guards the admin scanner,
// which selects invoiceRequestColumns plus u.username and u.email (2 extra).
// This is the regression that previously broke the 发票管理 page with
// "failed to scan invoice request" after migration 145 added 5 columns.
func TestScanAdminInvoiceRequest_DestCountMatchesColumns(t *testing.T) {
	s := &destCountScanner{}
	_, _, _, err := scanAdminInvoiceRequest(s)
	require.ErrorIs(t, err, errStopScan)
	require.Equal(t, invoiceColumnCount()+2, s.n,
		"scanAdminInvoiceRequest destinations must match invoiceRequestColumns + (username, email)")
}
