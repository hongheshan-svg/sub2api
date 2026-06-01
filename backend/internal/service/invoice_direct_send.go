package service

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"mime/multipart"
	"net/mail"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// SendInvoiceEmailInput 是对公直发发票邮件的入参。
type SendInvoiceEmailInput struct {
	RecipientEmail string
	Subject        string
	Note           string
	Files          []*multipart.FileHeader
}

// SendInvoiceEmail 直接把上传的多附件发到指定邮箱(与订单/余额解耦),并记录发送结果。
func (s *PaymentService) SendInvoiceEmail(ctx context.Context, adminID int64, input SendInvoiceEmailInput) error {
	to := strings.TrimSpace(input.RecipientEmail)
	if to == "" {
		return infraerrors.BadRequest("INVOICE_EMAIL_REQUIRED", "recipient email is required")
	}
	if _, err := mail.ParseAddress(to); err != nil {
		return infraerrors.BadRequest("INVOICE_EMAIL_INVALID", "recipient email is invalid")
	}
	if len(input.Files) == 0 {
		return infraerrors.BadRequest("INVOICE_FILE_REQUIRED", "at least one attachment is required")
	}
	if len(input.Files) > maxInvoiceAttachments {
		return infraerrors.BadRequest("INVOICE_FILE_TOO_MANY", "too many attachments")
	}
	if s.invoiceEmailSender == nil {
		return infraerrors.ServiceUnavailable("INVOICE_EMAIL_NOT_CONFIGURED", "invoice email sender is not configured")
	}

	attachments, err := readInvoiceAttachments(input.Files)
	if err != nil {
		return err
	}

	siteName := s.invoiceEmailSiteName(ctx)
	subject := strings.TrimSpace(input.Subject)
	if subject == "" {
		subject = fmt.Sprintf("[%s] 您的发票 / Your Invoice", siteName)
	}
	body := buildDirectInvoiceEmailBody(input.Note, siteName)

	names := make([]string, 0, len(attachments))
	for _, a := range attachments {
		names = append(names, a.Filename)
	}

	sendErr := s.invoiceEmailSender.SendEmailWithAttachment(ctx, to, subject, body, attachments)
	s.recordInvoiceEmailSend(ctx, adminID, to, subject, input.Note, names, sendErr)
	if sendErr != nil {
		return infraerrors.ServiceUnavailable("INVOICE_EMAIL_SEND_FAILED", "failed to send invoice email").WithCause(sendErr)
	}
	return nil
}

func (s *PaymentService) recordInvoiceEmailSend(ctx context.Context, adminID int64, to, subject, note string, names []string, sendErr error) {
	db, err := s.invoiceDB()
	if err != nil {
		return
	}
	status := "sent"
	var errMsg interface{} = nil
	if sendErr != nil {
		status = "failed"
		errMsg = sendErr.Error()
	}
	namesJSON, _ := json.Marshal(names)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO invoice_email_sends (recipient_email, subject, note, attachment_count, attachment_names, status, error_message, sent_by, sent_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9)
	`, to, subject, note, len(names), string(namesJSON), status, errMsg, adminID, time.Now()); err != nil {
		// 记录失败不影响主流程
		return
	}
}

func buildDirectInvoiceEmailBody(note, siteName string) string {
	site := html.EscapeString(siteName)
	noteHTML := ""
	if strings.TrimSpace(note) != "" {
		noteHTML = `<p>` + html.EscapeString(note) + `</p>`
	}
	return `<!DOCTYPE html><html><body style="font-family:Arial,sans-serif;color:#333;">
<p>您好，</p>
<p>您的发票文件已作为附件随本邮件发送，请查收。</p>` + noteHTML + `
<p>This email contains your invoice file(s) as attachments.</p>
<hr style="border:none;border-top:1px solid #eee;">
<p style="font-size:12px;color:#999;">本邮件由 ` + site + ` 系统发送，请勿直接回复。</p>
</body></html>`
}
