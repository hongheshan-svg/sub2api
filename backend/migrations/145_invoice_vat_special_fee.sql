-- 145_invoice_vat_special_fee.sql
-- 专票 6% 开票费 + 技术服务费类目:在 invoice_requests 增加金额拆分与类目列。
-- total_amount 保持不变(=申请金额合计),新逻辑读 invoice_amount。

ALTER TABLE invoice_requests
    ADD COLUMN IF NOT EXISTS base_amount      DECIMAL(20,2) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS fee_rate         DECIMAL(5,4)  NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS fee_amount       DECIMAL(20,2) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS invoice_amount   DECIMAL(20,2) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS service_category VARCHAR(64)   NOT NULL DEFAULT '技术服务费';

-- 回填存量行:申请金额与实际开票金额都等于历史 total_amount,费用为 0。
UPDATE invoice_requests
SET base_amount = total_amount,
    invoice_amount = total_amount
WHERE base_amount = 0 AND total_amount <> 0;
