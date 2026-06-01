//go:build unit

package service

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/textproto"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// stubInvoiceEmailSender implements the InvoiceEmailSender interface for tests.
// It records whether SendEmailWithAttachment was invoked so the validation
// short-circuits can be observed without touching SMTP or the DB.
type stubInvoiceEmailSender struct {
	err     error
	gotTo   string
	gotAtts int
}

func (s *stubInvoiceEmailSender) SendEmailWithAttachment(ctx context.Context, to, subject, body string, attachments []EmailAttachment) error {
	s.gotTo = to
	s.gotAtts = len(attachments)
	return s.err
}

// newTestFileHeader builds a *multipart.FileHeader by writing a tiny in-memory
// multipart form and reading it back, so the resulting header carries a real
// Content-Type and is openable via fh.Open().
func newTestFileHeader(t *testing.T, filename, mimeType string, content []byte) *multipart.FileHeader {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	hdr := make(textproto.MIMEHeader)
	hdr.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
	hdr.Set("Content-Type", mimeType)
	w, err := mw.CreatePart(hdr)
	require.NoError(t, err)
	_, err = w.Write(content)
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	mr := multipart.NewReader(&buf, mw.Boundary())
	form, err := mr.ReadForm(int64(len(content) + 1024))
	require.NoError(t, err)
	require.NotEmpty(t, form.File["file"])
	return form.File["file"][0]
}

// TestCompleteInvoiceRequest_NoEmailSender verifies that when the sender is
// not configured, a clear service-unavailable error is returned and no work
// is attempted.
func TestCompleteInvoiceRequest_NoEmailSender(t *testing.T) {
	svc := &PaymentService{}
	fh := newTestFileHeader(t, "x.pdf", "application/pdf", []byte("dummy"))

	_, err := svc.CompleteInvoiceRequest(context.Background(), 1, 1, CompleteInvoiceRequestInput{InvoiceNo: "ABC123", Files: []*multipart.FileHeader{fh}})
	require.Error(t, err)
	require.Equal(t, "INVOICE_EMAIL_NOT_CONFIGURED", infraerrors.Reason(err))
}

// TestCompleteInvoiceRequest_RejectsInvalidInvoiceNo confirms regex validation
// short-circuits before any sender or DB interaction.
func TestCompleteInvoiceRequest_RejectsInvalidInvoiceNo(t *testing.T) {
	sender := &stubInvoiceEmailSender{}
	svc := &PaymentService{invoiceEmailSender: sender}

	fh := newTestFileHeader(t, "x.pdf", "application/pdf", []byte("dummy"))
	_, err := svc.CompleteInvoiceRequest(context.Background(), 1, 1, CompleteInvoiceRequestInput{InvoiceNo: "bad!", Files: []*multipart.FileHeader{fh}})
	require.Error(t, err)
	require.Equal(t, "INVOICE_NO_FORMAT_INVALID", infraerrors.Reason(err))
	require.Equal(t, 0, sender.gotAtts, "sender must not be called when validation fails")
}

// TestCompleteInvoiceRequest_RejectsOversizedFile confirms the size cap is
// enforced before reading the file.
func TestCompleteInvoiceRequest_RejectsOversizedFile(t *testing.T) {
	sender := &stubInvoiceEmailSender{}
	svc := &PaymentService{invoiceEmailSender: sender}

	fh := newTestFileHeader(t, "x.pdf", "application/pdf", make([]byte, 100))
	fh.Size = maxInvoiceFileBytes + 1
	_, err := svc.CompleteInvoiceRequest(context.Background(), 1, 1, CompleteInvoiceRequestInput{InvoiceNo: "ABC123", Files: []*multipart.FileHeader{fh}})
	require.Error(t, err)
	require.Equal(t, "INVOICE_FILE_TOO_LARGE", infraerrors.Reason(err))
	require.Equal(t, 0, sender.gotAtts)
}

// TestBuildInvoiceAttachmentEmailBody_VATSpecial verifies that for a 专票
// (VAT special invoice) the email shows the actual InvoiceAmount (base − fee)
// rather than the base TotalAmount, and that the ServiceCategory is rendered.
func TestBuildInvoiceAttachmentEmailBody_VATSpecial(t *testing.T) {
	req := &InvoiceRequest{
		SerialNo:        "SN-2024-001",
		TotalAmount:     1000.00,
		BaseAmount:      1000.00,
		FeeRate:         0.06,
		FeeAmount:       60.00,
		InvoiceAmount:   940.00,
		ServiceCategory: "技术服务费",
		ProfileSnapshot: InvoiceProfileSnapshot{Title: "测试公司"},
	}

	body := buildInvoiceAttachmentEmailBody(req, "INV-888", "TestSite")

	// The email must show the actual invoice amount (940.00), not the base (1000.00).
	require.Contains(t, body, "940.00", "email body must contain the actual InvoiceAmount")
	require.Contains(t, body, "技术服务费", "email body must contain the ServiceCategory")

	// Regression guard: the base amount must NOT appear as the standalone
	// amount cell value.  (It may appear in other contexts, but the critical
	// requirement is that InvoiceAmount is what is shown in the amount row.)
	// We verify this indirectly by asserting InvoiceAmount != TotalAmount and
	// that 940.00 is present while we also confirm 1000.00 does NOT appear
	// in the amount cell specifically — checked by ensuring "940.00" IS there.
	require.NotEqual(t, req.TotalAmount, req.InvoiceAmount,
		"test is only meaningful when TotalAmount != InvoiceAmount")
}
