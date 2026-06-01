-- 146_invoice_fee_balance.sql
-- Part1: 专票费改为从余额收取,记录扣费/退费时间(冻结+退费幂等)。
-- Part4: 移除普票,invoice_type 恒为 vat_special。

-- 1) 开票申请:扣费/退费时间戳
ALTER TABLE invoice_requests
    ADD COLUMN IF NOT EXISTS fee_charged_at  TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS fee_refunded_at TIMESTAMPTZ;

-- 2) 发票档案:默认值改专票 + 回填存量普票 + 约束
ALTER TABLE invoice_profiles
    ALTER COLUMN invoice_type SET DEFAULT 'vat_special';

UPDATE invoice_profiles
SET invoice_type = 'vat_special'
WHERE invoice_type <> 'vat_special';

ALTER TABLE invoice_profiles
    DROP CONSTRAINT IF EXISTS invoice_profiles_invoice_type_check;
ALTER TABLE invoice_profiles
    ADD CONSTRAINT invoice_profiles_invoice_type_check CHECK (invoice_type = 'vat_special');
