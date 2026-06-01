-- 147_invoice_email_sends.sql
-- 管理员对公直发发票邮件的发送记录(与订单/余额解耦)。
CREATE TABLE IF NOT EXISTS invoice_email_sends (
    id               BIGSERIAL PRIMARY KEY,
    recipient_email  VARCHAR(255) NOT NULL,
    subject          VARCHAR(255) NOT NULL DEFAULT '',
    note             TEXT,
    attachment_count INTEGER NOT NULL DEFAULT 0,
    attachment_names JSONB,
    status           VARCHAR(16) NOT NULL,
    error_message    TEXT,
    sent_by          BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    sent_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS invoice_email_sends_sent_at_idx
    ON invoice_email_sends(sent_at DESC);
