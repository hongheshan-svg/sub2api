# 发票改造实施计划（专票全额+余额扣6% / 移除普票 / 多附件 / 对公直发）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 专票按全额开票并在提交时从用户余额扣 6% 服务费（不足则拦截、可退费、扣费前二次确认），彻底移除普票只留专票，开票完成与对公直发都支持多附件邮件。

**Architecture:** 沿用现有「raw `database/sql` 事务 + ent 同库」的发票实现：余额扣减用守卫式原始 SQL（`balance >= fee`）在开票事务内原子完成，提交后经新注入的 `*UserService` 失效余额缓存。移除普票采用「保留 `invoice_type` 列、后端恒写 `vat_special`、迁移回填 + CHECK」。多附件把单文件入参改为切片；对公直发是与订单/余额解耦的新接口 + 记录表。

**Tech Stack:** Go (gin, lib/pq, database/sql, entgo)、Postgres 迁移 SQL、Vue 3 + TS（Pinia、vue-i18n、vitest）。

**对应 spec：** `docs/superpowers/specs/2026-06-01-invoice-fee-balance-and-direct-send-design.md`

---

## 执行顺序与可发布单元

- **Phase A（后端）+ Phase B（前端）= 一个不可分割的发布单元**：A 让后端强制专票并对所有档案要求银行字段；若不同时上线 B，前端仍发普票/缺字段会被拒。**A、B 必须一起合并上线。**
- **Phase C（开票完成多附件）** 与 **Phase D（对公直发）** 各自独立、可单独上线。
- 建议顺序：A → B → C → D。

## 文件结构总览

**Phase A/B（专票全额+余额扣费、移除普票）**
- `backend/migrations/146_invoice_fee_balance.sql`（新建）：`invoice_requests` 加 `fee_charged_at/fee_refunded_at`；`invoice_profiles.invoice_type` 默认值改 `vat_special` + 回填 + CHECK。
- `backend/internal/service/invoice_amount.go`（改）：`computeInvoiceAmounts` 全额语义。
- `backend/internal/service/invoice_fee.go`（新建）：纯函数 `invoiceFeeShortfall`。
- `backend/internal/service/invoice_service.go`（改）：`validInvoiceTypes` 只留专票、`normalizeInvoiceProfileInput` 恒专票+必填、`CreateInvoiceRequest` 扣费+拦截。
- `backend/internal/service/invoice_admin_service.go`（改）：`RejectInvoiceRequest`/`CancelInvoiceRequest` 退费。
- `backend/internal/service/payment_service.go`（改）：加 `userService` 字段 + `SetUserService`。
- `backend/internal/service/user_service.go`（改）：抽出公开方法 `InvalidateBalanceCaches`。
- `backend/cmd/server/wire_gen.go`（改）：`paymentService.SetUserService(userService)`。
- `backend/internal/service/invoice_amount_test.go`（改）、`backend/internal/service/invoice_fee_test.go`（新建）。
- 前端：`types/invoice.ts`、`api/invoice.ts`、`views/user/InvoiceView.vue`、`stores/app.ts`、`i18n/locales/zh.ts`、`i18n/locales/en.ts`、`views/admin/AdminInvoicesView.vue`（类型徽标简化）。

**Phase C（开票完成多附件）**
- `backend/internal/service/invoice_admin_service.go`（改）：`CompleteInvoiceRequestInput.Files []*multipart.FileHeader` + 多附件发送。
- `backend/internal/handler/admin/invoice_handler.go`（改）：解析多文件。
- 前端：`api/admin/invoices.ts`、`views/admin/AdminInvoicesView.vue`（多选文件）。

**Phase D（对公直发）**
- `backend/migrations/147_invoice_email_sends.sql`（新建）。
- `backend/internal/service/invoice_direct_send.go`（新建）：`SendInvoiceEmail` + 记录写入。
- `backend/internal/handler/admin/invoice_handler.go`（改）：新 handler 方法。
- `backend/internal/server/routes/payment.go`（改）：注册路由。
- 前端：`api/admin/invoices.ts`、`views/admin/AdminInvoicesView.vue`（直发表单 + 历史）。

## 测试说明（重要）

发票核心路径用 **Postgres 专属原始 SQL**（`$1`、`pq.Array`、`::jsonb`、`::float8`、`RETURNING`），项目里 `enttest` 是 SQLite，**无法**覆盖这些路径——现有唯一的发票单测是 `invoice_amount_test.go`（纯函数）。因此：
- **纯函数逻辑走 TDD**（`computeInvoiceAmounts`、`invoiceFeeShortfall`）。
- **触库逻辑**（扣费/退费/多附件/直发）以「实现 + `go build ./...` + `go vet` + 手工验证（curl/SQL）」验收，不写跑不起来的 DB 单测。每个相关任务给出明确手工验证步骤。

---

# Phase A：后端 —— 专票全额 + 余额扣 6% + 移除普票

### Task A1: 迁移 146（金额冻结列 + 移除普票数据）

**Files:**
- Create: `backend/migrations/146_invoice_fee_balance.sql`

- [ ] **Step 1: 写迁移文件**

```sql
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
```

- [ ] **Step 2: 校验迁移语法（如有本地 DB）**

Run: `cd backend && grep -c "vat_special" migrations/146_invoice_fee_balance.sql`
Expected: 输出 `3`（确认文件已写入，3 处 vat_special）。
（迁移会在应用启动时按编号顺序自动执行;若有本地 Postgres，可手动 `psql -f` 验证无语法错误。）

- [ ] **Step 3: Commit**

```bash
cd backend && git add migrations/146_invoice_fee_balance.sql
git commit -m "feat(invoice): migration 146 - fee charge/refund timestamps + force vat_special"
```

---

### Task A2: `computeInvoiceAmounts` 改为全额语义（TDD）

**Files:**
- Modify: `backend/internal/service/invoice_amount.go:21-30`
- Test: `backend/internal/service/invoice_amount_test.go`

- [ ] **Step 1: 改写测试为全额语义**

打开 `backend/internal/service/invoice_amount_test.go`，把 `computeInvoiceAmounts` 的断言改为：专票全额开票、费用为额外项（`invoice == base`，`fee == base*rate`）。替换该测试函数体为：

```go
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
```

（如果文件里 `resolveInvoiceFeeConfig` 的测试用到 `InvoiceTypeGeneral` 返回 0，保留它——历史快照仍可能是 general，函数对 general 返回 0 是正确的安全行为。）

- [ ] **Step 2: 运行测试，确认失败**

Run: `cd backend && go test ./internal/service/ -run TestComputeInvoiceAmounts -v`
Expected: FAIL（当前实现返回 invoice=940）。

- [ ] **Step 3: 改实现**

把 `backend/internal/service/invoice_amount.go` 的 `computeInvoiceAmounts` 替换为：

```go
// computeInvoiceAmounts 由申请金额与费率计算开票费与实际开票金额。
// 新语义:专票按全额开票(invoiceAmount=base),费用为额外向余额收取的附加项(base*rate)。
func computeInvoiceAmounts(base, feeRate float64) (feeAmount, invoiceAmount float64) {
	invoiceAmount = round2(base)
	if feeRate <= 0 || base <= 0 {
		return 0, invoiceAmount
	}
	feeAmount = round2(base * feeRate)
	return feeAmount, invoiceAmount
}
```

- [ ] **Step 4: 运行测试，确认通过**

Run: `cd backend && go test ./internal/service/ -run 'TestComputeInvoiceAmounts|TestResolveInvoiceFeeConfig' -v`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
cd backend && git add internal/service/invoice_amount.go internal/service/invoice_amount_test.go
git commit -m "feat(invoice): full-amount invoice semantics, fee is additive"
```

---

### Task A3: 余额不足纯函数 `invoiceFeeShortfall`（TDD）

**Files:**
- Create: `backend/internal/service/invoice_fee.go`
- Test: `backend/internal/service/invoice_fee_test.go`

- [ ] **Step 1: 写失败测试**

```go
package service

import "testing"

func TestInvoiceFeeShortfall(t *testing.T) {
	// 余额足够
	if ok, short := invoiceFeeShortfall(100, 60); !ok || short != 0 {
		t.Fatalf("enough: want ok=true short=0, got ok=%v short=%.2f", ok, short)
	}
	// 余额不足
	if ok, short := invoiceFeeShortfall(20, 60); ok || short != 40 {
		t.Fatalf("short: want ok=false short=40, got ok=%v short=%.2f", ok, short)
	}
	// 恰好相等
	if ok, short := invoiceFeeShortfall(60, 60); !ok || short != 0 {
		t.Fatalf("equal: want ok=true short=0, got ok=%v short=%.2f", ok, short)
	}
	// fee<=0:无需扣费
	if ok, short := invoiceFeeShortfall(0, 0); !ok || short != 0 {
		t.Fatalf("nofee: want ok=true short=0, got ok=%v short=%.2f", ok, short)
	}
}
```

- [ ] **Step 2: 运行，确认失败**

Run: `cd backend && go test ./internal/service/ -run TestInvoiceFeeShortfall -v`
Expected: FAIL（`invoiceFeeShortfall` 未定义）。

- [ ] **Step 3: 写实现**

```go
package service

// invoiceFeeShortfall 判断余额能否覆盖开票服务费。
// 返回 ok=true 表示足够;否则返回还差多少(shortfall,保留 2 位)。
func invoiceFeeShortfall(balance, fee float64) (ok bool, shortfall float64) {
	if fee <= 0 {
		return true, 0
	}
	if balance >= fee {
		return true, 0
	}
	return false, round2(fee - balance)
}
```

- [ ] **Step 4: 运行，确认通过**

Run: `cd backend && go test ./internal/service/ -run TestInvoiceFeeShortfall -v`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
cd backend && git add internal/service/invoice_fee.go internal/service/invoice_fee_test.go
git commit -m "feat(invoice): invoiceFeeShortfall helper"
```

---

### Task A4: `UserService.InvalidateBalanceCaches` 公开方法

**Files:**
- Modify: `backend/internal/service/user_service.go:1057-1080`

- [ ] **Step 1: 抽出公开失效方法，并让 UpdateBalance 复用**

把 `UpdateBalance`（约 1057-1080 行）改为：

```go
// UpdateBalance 更新用户余额（管理员功能）
func (s *UserService) UpdateBalance(ctx context.Context, userID int64, amount float64) error {
	if err := s.userRepo.UpdateBalance(ctx, userID, amount); err != nil {
		return fmt.Errorf("update balance: %w", err)
	}
	s.InvalidateBalanceCaches(ctx, userID)
	return nil
}

// InvalidateBalanceCaches 失效用户的鉴权缓存与计费余额缓存。
// 供「在事务外已直接改动 users.balance」的调用方(如发票扣费)复用。
func (s *UserService) InvalidateBalanceCaches(ctx context.Context, userID int64) {
	if s == nil {
		return
	}
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	if s.billingCache != nil {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("panic in balance cache invalidation", "user_id", userID, "recover", r)
				}
			}()
			cacheCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.billingCache.InvalidateUserBalance(cacheCtx, userID); err != nil {
				slog.Error("invalidate user balance cache failed", "user_id", userID, "error", err)
			}
		}()
	}
}
```

- [ ] **Step 2: 编译**

Run: `cd backend && go build ./internal/service/`
Expected: 无错误。

- [ ] **Step 3: Commit**

```bash
cd backend && git add internal/service/user_service.go
git commit -m "refactor(user): extract InvalidateBalanceCaches for reuse"
```

---

### Task A5: PaymentService 注入 UserService + 接线

**Files:**
- Modify: `backend/internal/service/payment_service.go:177-194`（结构体 + 新 setter）
- Modify: `backend/cmd/server/wire_gen.go:245`（接线）

- [ ] **Step 1: 结构体加字段**

在 `PaymentService` 结构体（177-194 行）末尾，`invoiceEmailSender` 之后加一行：

```go
	invoiceEmailSender       InvoiceEmailSender
	userService              *UserService
```

- [ ] **Step 2: 加 setter**

在 `SetInvoiceEmailSender`（224-229 行）之后追加：

```go
// SetUserService 注入 UserService,供发票扣费后失效余额缓存。
func (s *PaymentService) SetUserService(us *UserService) {
	if s == nil {
		return
	}
	s.userService = us
}
```

- [ ] **Step 3: wire_gen 接线**

在 `backend/cmd/server/wire_gen.go` 第 245 行（`paymentService.SetInvoiceEmailSender(emailService)`）之后插入一行（`userService` 已在第 77 行定义）：

```go
	paymentService.SetInvoiceEmailSender(emailService)
	paymentService.SetUserService(userService)
```

- [ ] **Step 4: 编译**

Run: `cd backend && go build ./...`
Expected: 无错误。

- [ ] **Step 5: Commit**

```bash
cd backend && git add internal/service/payment_service.go cmd/server/wire_gen.go
git commit -m "feat(invoice): inject UserService into PaymentService for cache invalidation"
```

---

### Task A6: 移除普票——`validInvoiceTypes` 与档案校验恒专票

**Files:**
- Modify: `backend/internal/service/invoice_service.go:30-34`（validInvoiceTypes）
- Modify: `backend/internal/service/invoice_service.go:152-203`（normalizeInvoiceProfileInput）

- [ ] **Step 1: 收窄 validInvoiceTypes**

把 30-34 行替换为：

```go
// validInvoiceTypes lists all allowed invoice_type values.
// 普票已移除:仅专票为合法输入(InvoiceTypeGeneral 常量保留,仅用于读历史快照)。
var validInvoiceTypes = map[string]bool{
	InvoiceTypeVATSpecial: true,
}
```

- [ ] **Step 2: normalize 恒写专票 + 必填字段始终生效**

把 `normalizeInvoiceProfileInput` 的类型处理（161-201 行）替换为：忽略传入类型、恒写 `vat_special`、银行/地址/电话始终必填。即把 161-201 行替换为：

```go
	// 普票已移除:忽略传入的 invoice_type,一律按专票处理。
	input.InvoiceType = InvoiceTypeVATSpecial

	switch {
	case input.Title == "":
		return input, infraerrors.BadRequest("INVOICE_TITLE_REQUIRED", "invoice title is required")
	case len(input.Title) > 255:
		return input, infraerrors.BadRequest("INVOICE_TITLE_TOO_LONG", "invoice title is too long")
	case input.TaxNumber == "":
		return input, infraerrors.BadRequest("INVOICE_TAX_NUMBER_REQUIRED", "invoice tax number is required")
	case len(input.TaxNumber) > 64:
		return input, infraerrors.BadRequest("INVOICE_TAX_NUMBER_TOO_LONG", "invoice tax number is too long")
	case input.Email == "":
		return input, infraerrors.BadRequest("INVOICE_EMAIL_REQUIRED", "invoice email is required")
	case len(input.Email) > 255:
		return input, infraerrors.BadRequest("INVOICE_EMAIL_TOO_LONG", "invoice email is too long")
	}
	if _, err := mail.ParseAddress(input.Email); err != nil {
		return input, infraerrors.BadRequest("INVOICE_EMAIL_INVALID", "invoice email is invalid")
	}

	// 专票必填:开户行、账号、注册地址、电话(所有档案均为专票,故恒校验)。
	if input.Address == nil || strings.TrimSpace(*input.Address) == "" {
		return input, infraerrors.BadRequest("INVOICE_VAT_ADDRESS_REQUIRED", "registered address is required for VAT special invoice")
	}
	if input.Phone == nil || strings.TrimSpace(*input.Phone) == "" {
		return input, infraerrors.BadRequest("INVOICE_VAT_PHONE_REQUIRED", "phone is required for VAT special invoice")
	}
	if input.BankName == nil || strings.TrimSpace(*input.BankName) == "" {
		return input, infraerrors.BadRequest("INVOICE_VAT_BANK_NAME_REQUIRED", "bank name is required for VAT special invoice")
	}
	if input.BankAccount == nil || strings.TrimSpace(*input.BankAccount) == "" {
		return input, infraerrors.BadRequest("INVOICE_VAT_BANK_ACCOUNT_REQUIRED", "bank account is required for VAT special invoice")
	}
	return input, nil
```

- [ ] **Step 3: 编译**

Run: `cd backend && go build ./internal/service/`
Expected: 无错误（`InvoiceTypeGeneral` 常量仍被引用于 `resolveInvoiceFeeConfig` 的安全分支与历史快照，保留即可）。

- [ ] **Step 4: Commit**

```bash
cd backend && git add internal/service/invoice_service.go
git commit -m "feat(invoice): force vat_special, drop general from profile validation"
```

---

### Task A7: `CreateInvoiceRequest` 提交时扣费 + 余额不足拦截

**Files:**
- Modify: `backend/internal/service/invoice_service.go:486-523`

- [ ] **Step 1: 在事务内扣费、INSERT 带 fee_charged_at、提交后失效缓存**

把 `CreateInvoiceRequest` 中从 `totalAmount := 0.0`（491 行）到 `return &req, nil`（523 行）之间替换为下面内容（核心新增:扣费守卫 SQL + INSERT 增加 `fee_charged_at` 列 + 提交后失效缓存）：

```go
	totalAmount := 0.0
	for _, order := range orders {
		totalAmount += order.PayAmount
	}
	totalAmount = round2(totalAmount)
	feeRate, serviceCategory := s.resolveInvoiceFeeConfig(ctx, snapshot.InvoiceType)
	feeAmount, invoiceAmount := computeInvoiceAmounts(totalAmount, feeRate)

	// 专票服务费从余额扣除:余额不足则拦截要求充值。
	var feeChargedAt interface{} = nil
	if feeAmount > 0 {
		res, err := tx.ExecContext(ctx, `
			UPDATE users SET balance = balance - $1 WHERE id = $2 AND balance >= $1
		`, feeAmount, userID)
		if err != nil {
			return nil, infraerrors.InternalServer("INVOICE_REQUEST_CREATE_FAILED", "failed to charge invoice fee").WithCause(err)
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			// 读取当前余额以构造提示
			var balance float64
			_ = tx.QueryRowContext(ctx, `SELECT balance::float8 FROM users WHERE id = $1`, userID).Scan(&balance)
			_, shortfall := invoiceFeeShortfall(balance, feeAmount)
			return nil, infraerrors.BadRequest(
				"INVOICE_BALANCE_INSUFFICIENT",
				fmt.Sprintf("余额不足以支付开票服务费:需 ¥%.2f,当前余额 ¥%.2f,还差 ¥%.2f,请先充值后再提交。 / insufficient balance for invoice fee", feeAmount, balance, shortfall),
			)
		}
		feeChargedAt = time.Now()
	}

	serialNo := generateInvoiceSerialNo()
	row := tx.QueryRowContext(ctx, `
		INSERT INTO invoice_requests (user_id, profile_id, serial_no, status, profile_snapshot, total_amount, base_amount, fee_rate, fee_amount, invoice_amount, service_category, fee_charged_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9, $10, $11, $12)
		RETURNING `+invoiceRequestColumns,
		userID, input.ProfileID, serialNo, InvoiceStatusPending, string(snapshotBytes),
		totalAmount, totalAmount, feeRate, feeAmount, invoiceAmount, serviceCategory, feeChargedAt)
	req, err := scanInvoiceRequest(row)
	if err != nil {
		return nil, err
	}
	for _, order := range orders {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO invoice_request_orders (invoice_request_id, payment_order_id)
			VALUES ($1, $2)
		`, req.ID, order.ID); err != nil {
			return nil, infraerrors.InternalServer("INVOICE_REQUEST_CREATE_FAILED", "failed to attach invoice order").WithCause(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, infraerrors.InternalServer("INVOICE_REQUEST_CREATE_FAILED", "failed to commit invoice request").WithCause(err)
	}
	if feeAmount > 0 && s.userService != nil {
		s.userService.InvalidateBalanceCaches(ctx, userID)
	}
	req.Orders = orders
	return &req, nil
```

- [ ] **Step 2: 确认 `time` 与 `fmt` 已导入**

`invoice_service.go` 顶部已 import `time` 与 `fmt`（见文件头 4-10 行）。无需改动。

- [ ] **Step 3: 编译**

Run: `cd backend && go build ./...`
Expected: 无错误。

- [ ] **Step 4: 手工验证（需本地运行 + Postgres）**

1. 准备一个余额=0 的用户、一笔已完成订单、一个专票档案。
2. `POST /api/v1/invoice/requests` → 期望 400，错误码 `INVOICE_BALANCE_INSUFFICIENT`，消息含「还差 ¥…」。`SELECT count(*) FROM invoice_requests` 不增加；用户 `balance` 不变。
3. 给该用户充值到 ≥ 费用，再次提交 → 期望 200；`SELECT balance FROM users` 减少了 `fee_amount`；该行 `fee_charged_at` 非空、`invoice_amount = base_amount`、`fee_amount = base*rate`。

- [ ] **Step 5: Commit**

```bash
cd backend && git add internal/service/invoice_service.go
git commit -m "feat(invoice): charge vat fee from balance on submit, block if insufficient"
```

---

### Task A8: 驳回/取消时退费

**Files:**
- Modify: `backend/internal/service/invoice_admin_service.go:216-294`（Cancel 与 Reject）

- [ ] **Step 1: CancelInvoiceRequest 删除前退费**

在 `CancelInvoiceRequest` 中，把「校验 status 为 pending 之后、删除 invoice_request_orders 之前」插入退费逻辑。即在第 241 行（`}` 结束 status 校验）之后、第 243 行（DELETE invoice_request_orders）之前插入：

```go
		// 提交时已扣的专票服务费需退回(幂等:仅当已扣未退)。
		var feeAmount float64
		var feeChargedAt, feeRefundedAt sql.NullTime
		if err := tx.QueryRowContext(ctx, `
			SELECT fee_amount::float8, fee_charged_at, fee_refunded_at
			FROM invoice_requests WHERE id = $1 FOR UPDATE
		`, reqID).Scan(&feeAmount, &feeChargedAt, &feeRefundedAt); err != nil {
			return infraerrors.InternalServer("INVOICE_REQUEST_CANCEL_FAILED", "failed to load invoice fee").WithCause(err)
		}
		if feeAmount > 0 && feeChargedAt.Valid && !feeRefundedAt.Valid {
			if _, err := tx.ExecContext(ctx, `UPDATE users SET balance = balance + $1 WHERE id = $2`, feeAmount, userID); err != nil {
				return infraerrors.InternalServer("INVOICE_REQUEST_CANCEL_FAILED", "failed to refund invoice fee").WithCause(err)
			}
		}
```

并在 `tx.Commit()` 成功之后（第 251 行 `return nil` 之前）插入缓存失效：

```go
	if s.userService != nil {
		s.userService.InvalidateBalanceCaches(ctx, userID)
	}
	return nil
```

- [ ] **Step 2: RejectInvoiceRequest 改为事务 + 退费 + 写 fee_refunded_at**

把 `RejectInvoiceRequest`（266-293 行，从 `db, err := s.invoiceDB()` 到 `return &req, nil`）替换为事务版本：

```go
	db, err := s.invoiceDB()
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, infraerrors.InternalServer("INVOICE_REQUEST_REJECT_FAILED", "failed to begin reject transaction").WithCause(err)
	}
	defer rollbackIfActive(tx)

	// 锁定并读取扣费状态
	var userID int64
	var feeAmount float64
	var status string
	var feeChargedAt, feeRefundedAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `
		SELECT user_id, status, fee_amount::float8, fee_charged_at, fee_refunded_at
		FROM invoice_requests WHERE id = $1 FOR UPDATE
	`, reqID).Scan(&userID, &status, &feeAmount, &feeChargedAt, &feeRefundedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, infraerrors.NotFound("INVOICE_REQUEST_NOT_FOUND", "invoice request not found")
		}
		return nil, infraerrors.InternalServer("INVOICE_REQUEST_REJECT_FAILED", "failed to load invoice request").WithCause(err)
	}
	if status != InvoiceStatusPending {
		return nil, infraerrors.Conflict("INVOICE_REQUEST_NOT_PENDING", "invoice request is not pending")
	}

	if feeAmount > 0 && feeChargedAt.Valid && !feeRefundedAt.Valid {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET balance = balance + $1 WHERE id = $2`, feeAmount, userID); err != nil {
			return nil, infraerrors.InternalServer("INVOICE_REQUEST_REJECT_FAILED", "failed to refund invoice fee").WithCause(err)
		}
	}

	row := tx.QueryRowContext(ctx, `
		UPDATE invoice_requests
		SET status = $2,
		    reject_reason = $3,
		    processed_by = $4,
		    processed_at = NOW(),
		    updated_at = NOW(),
		    fee_refunded_at = CASE WHEN fee_charged_at IS NOT NULL AND fee_refunded_at IS NULL THEN NOW() ELSE fee_refunded_at END
		WHERE id = $1 AND status = $5
		RETURNING `+invoiceRequestColumns,
		reqID, InvoiceStatusRejected, reason, adminID, InvoiceStatusPending)
	req, err := scanInvoiceRequest(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, invoiceRequestStateError(ctx, db, reqID)
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, infraerrors.InternalServer("INVOICE_REQUEST_REJECT_FAILED", "failed to commit reject").WithCause(err)
	}
	if feeAmount > 0 && s.userService != nil {
		s.userService.InvalidateBalanceCaches(ctx, userID)
	}

	orders, err := queryInvoiceRequestOrders(ctx, db, []int64{req.ID})
	if err != nil {
		return nil, err
	}
	req.Orders = orders[req.ID]
	return &req, nil
```

- [ ] **Step 3: 编译**

Run: `cd backend && go build ./...`
Expected: 无错误（`sql`、`errors` 已在该文件导入）。

- [ ] **Step 4: 手工验证**

1. 用 A7 的成功路径造一条 pending 专票（已扣费），记下用户余额 `B`。
2. 管理员驳回 → 用户余额回到 `B + fee`；该行 `status=rejected`、`fee_refunded_at` 非空。再次驳回应报「not pending」，余额不再变化（幂等）。
3. 另造一条 pending 专票（已扣费），用户取消 → 行被删除、余额回到 `B + fee`。

- [ ] **Step 5: Commit**

```bash
cd backend && git add internal/service/invoice_admin_service.go
git commit -m "feat(invoice): refund vat fee on reject/cancel (idempotent)"
```

---

# Phase B：前端 —— 移除普票、余额预览、二次确认、待补全

### Task B1: 类型与 i18n 收窄/改写

**Files:**
- Modify: `frontend/src/types/invoice.ts:2`
- Modify: `frontend/src/i18n/locales/zh.ts`（invoice.invoiceTypes / invoice.fee.notice）
- Modify: `frontend/src/i18n/locales/en.ts`（同上）

- [ ] **Step 1: 收窄 InvoiceType**

`frontend/src/types/invoice.ts` 第 2 行：

```ts
export type InvoiceType = 'vat_special'
```

- [ ] **Step 2: zh.ts 改文案**

在 `frontend/src/i18n/locales/zh.ts` 的 `invoice` 对象内：删除 `invoiceTypes.general` 一行；把 `fee.notice` 改为全额口径，并新增余额相关文案：

```js
    invoiceTypes: {
      vat_special: '增值税专用发票',
    },
    fee: {
      noticeTitle: '专票须知',
      notice: '增值税专用发票按申请全额开具,需额外支付 {rate}% 开票服务费(¥{fee}),将从账户余额扣除;开票类目为{category}。',
      balanceLine: '当前余额 ¥{balance},扣后 ¥{after}。',
      insufficient: '余额不足,开票服务费需 ¥{fee},当前余额 ¥{balance},还差 ¥{shortfall},请先充值。',
      confirmTitle: '确认支付开票服务费',
      confirmBody: '开具增值税专用发票需支付 {rate}% 开票服务费 ¥{fee},将从账户余额扣除(余额 ¥{balance} → ¥{after})。确认提交?',
      confirmOk: '确认并扣费',
      deducted: '开票申请已提交,已扣除开票服务费 ¥{fee}。',
      incompleteProfile: '该抬头缺少专票必填信息(开户行/账号/地址/电话),请先补全后再开票。',
      incompleteBadge: '待补全',
    },
```

- [ ] **Step 3: en.ts 改文案（对应键）**

在 `frontend/src/i18n/locales/en.ts` 的 `invoice` 对象内做对应修改：

```js
    invoiceTypes: {
      vat_special: 'VAT Special Invoice',
    },
    fee: {
      noticeTitle: 'VAT Special Invoice Notice',
      notice: 'Issued for the full amount; a {rate}% service fee (¥{fee}) is charged from your balance. Category: {category}.',
      balanceLine: 'Balance ¥{balance}, after ¥{after}.',
      insufficient: 'Insufficient balance. Fee needs ¥{fee}, balance ¥{balance}, short ¥{shortfall}. Please top up first.',
      confirmTitle: 'Confirm invoice service fee',
      confirmBody: 'A {rate}% service fee ¥{fee} will be charged from your balance (¥{balance} → ¥{after}). Submit?',
      confirmOk: 'Confirm & charge',
      deducted: 'Invoice request submitted; ¥{fee} service fee charged.',
      incompleteProfile: 'This title is missing required VAT fields (bank/account/address/phone). Please complete it first.',
      incompleteBadge: 'Incomplete',
    },
```

- [ ] **Step 4: 类型检查**

Run: `cd frontend && npx vue-tsc --noEmit -p tsconfig.json 2>&1 | grep -i invoice | head`
Expected: 出现因 `InvoiceType` 收窄导致的报错（将在 B2/B3 修复）——确认报错集中在 InvoiceView.vue / app store。

- [ ] **Step 5: Commit**

```bash
cd frontend && git add src/types/invoice.ts src/i18n/locales/zh.ts src/i18n/locales/en.ts
git commit -m "feat(invoice): narrow InvoiceType to vat_special, rewrite fee i18n"
```

---

### Task B2: InvoiceView 移除普票选项、必填恒生效、待补全

**Files:**
- Modify: `frontend/src/views/user/InvoiceView.vue`

- [ ] **Step 1: 移除发票类型单选，固定专票**

删除 `invoiceTypeOptions` computed（约 623 行）。把模板里发票类型 radio-group 块（438-457 行）替换为一个只读说明：

```vue
<div class="md:col-span-2">
  <label class="input-label">{{ t('invoice.fields.invoiceType') }}</label>
  <p class="mt-1 text-sm text-gray-700 dark:text-gray-300">{{ t('invoice.invoiceTypes.vat_special') }}</p>
  <p class="mt-2 text-xs text-amber-600 dark:text-amber-400">{{ t('invoice.profiles.vatRequiredHint') }}</p>
</div>
```

- [ ] **Step 2: ProfileForm 默认值与编辑回填改专票**

- 表单接口（567-576 行）`invoice_type` 字段类型改为 `'vat_special'`。
- `profileForm` 默认（620 行）`invoice_type: 'vat_special'`。
- `openEditProfile`（889 行）改为 `profileForm.invoice_type = 'vat_special'`。
- payload builder（991 行）`invoice_type: 'vat_special'` —— 或直接删除该字段交由后端强制（后端已忽略传入值）。保留显式 `'vat_special'` 以满足 `InvoiceProfilePayload` 类型。

- [ ] **Step 3: 必填标记恒显示**

`isVATSpecial` computed（628 行）改为常量 `true`，或直接把模板里 phone/bank_name/bank_account/address 的 `v-if="isVATSpecial"`、`:required="isVATSpecial"`（481-507 行）改为 `required` 恒真、`<span class="text-red-500">*</span>` 恒显示。推荐：

```ts
const isVATSpecial = computed(() => true)
```

（保留 computed 名字，模板不动，改动最小。）

- [ ] **Step 4: 档案卡片徽标固定专票**

把档案卡片徽标（225-230 行）替换为固定专票样式，并对缺字段档案显示「待补全」：

```vue
<span class="rounded-full bg-purple-50 px-2 py-0.5 text-xs font-medium text-purple-700 dark:bg-purple-900/30 dark:text-purple-300">
  {{ t('invoice.invoiceTypes.vat_special') }}
</span>
<span v-if="isProfileIncomplete(profile)" class="ml-1 rounded-full bg-amber-100 px-2 py-0.5 text-xs font-medium text-amber-700 dark:bg-amber-900/30 dark:text-amber-300">
  {{ t('invoice.fee.incompleteBadge') }}
</span>
```

- [ ] **Step 5: 加 isProfileIncomplete 判定**

在 `<script setup>` 内（靠近其它 computed/函数）加入：

```ts
function isProfileIncomplete(p: InvoiceProfile): boolean {
  return !p.address || !p.phone || !p.bank_name || !p.bank_account
}
```

- [ ] **Step 6: 编译/类型检查**

Run: `cd frontend && npx vue-tsc --noEmit -p tsconfig.json 2>&1 | grep -i "InvoiceView" | head`
Expected: 无 InvoiceView 相关报错（fee 预览相关在 B3 处理；若此处仍报 previewInvoiceAmount 等，B3 一并修）。

- [ ] **Step 7: Commit**

```bash
cd frontend && git add src/views/user/InvoiceView.vue
git commit -m "feat(invoice): remove general option, require vat fields, mark incomplete profiles"
```

---

### Task B3: 余额预览、扣费口径、不足拦截、二次确认

**Files:**
- Modify: `frontend/src/views/user/InvoiceView.vue`

- [ ] **Step 1: 引入余额 + 改费用预览口径（全额）**

在 `<script setup>` 顶部确保引入 auth store：

```ts
import { useAuthStore } from '@/stores/auth'
const authStore = useAuthStore()
const userBalance = computed(() => authStore.user?.balance ?? 0)
```

把费用预览 computed（669-674 行）改为全额语义（开票金额=全额，费用为额外项）：

```ts
const isVatSpecialSelected = computed(() => !!selectedProfile.value) // 所有档案均为专票
const previewFeeRate = computed(() => appStore.invoiceVatSpecialFeeRate)
const previewFeeAmount = computed(() => Math.round(selectedTotal.value * previewFeeRate.value * 100) / 100)
const previewInvoiceAmount = computed(() => Math.round(selectedTotal.value * 100) / 100) // 全额
const previewFeePercent = computed(() => Math.round(previewFeeRate.value * 1000) / 10)
const previewBalanceAfter = computed(() => Math.round((userBalance.value - previewFeeAmount.value) * 100) / 100)
const feeShortfall = computed(() => Math.max(0, Math.round((previewFeeAmount.value - userBalance.value) * 100) / 100))
const balanceInsufficient = computed(() => previewFeeAmount.value > 0 && userBalance.value < previewFeeAmount.value)
```

- [ ] **Step 2: 更新费用/余额预览模板**

把 fee notice + 金额拆分块（319-352 行）替换为（全额口径 + 余额行 + 不足提示）：

```vue
<div
  v-if="isVatSpecialSelected && selectedTotal > 0"
  class="mt-3 rounded-lg border border-amber-300 bg-amber-50 p-3 text-sm text-amber-800 dark:border-amber-700/60 dark:bg-amber-900/20 dark:text-amber-200"
>
  <div class="font-semibold">⚠ {{ t('invoice.fee.noticeTitle') }}</div>
  <div class="mt-1">
    {{ t('invoice.fee.notice', { rate: previewFeePercent, fee: formatCurrency(previewFeeAmount), category: appStore.invoiceServiceCategory }) }}
  </div>
  <div class="mt-1">
    {{ t('invoice.fee.balanceLine', { balance: formatCurrency(userBalance), after: formatCurrency(previewBalanceAfter) }) }}
  </div>
  <div v-if="balanceInsufficient" class="mt-2 font-semibold text-red-600 dark:text-red-400">
    {{ t('invoice.fee.insufficient', { fee: formatCurrency(previewFeeAmount), balance: formatCurrency(userBalance), shortfall: formatCurrency(feeShortfall) }) }}
    <RouterLink to="/recharge" class="underline">{{ t('common.recharge') || '去充值' }}</RouterLink>
  </div>
</div>

<div class="mt-3 space-y-1 text-sm">
  <div class="flex justify-between">
    <span class="text-gray-500 dark:text-gray-400">{{ t('invoice.fields.baseAmount') }}</span>
    <span>{{ formatCurrency(selectedTotal) }}</span>
  </div>
  <div class="flex justify-between font-semibold border-t border-gray-200 dark:border-dark-700 pt-1">
    <span>{{ t('invoice.fields.invoiceAmount') }}</span>
    <span>{{ formatCurrency(previewInvoiceAmount) }}</span>
  </div>
  <div class="flex justify-between text-amber-700 dark:text-amber-300">
    <span>{{ t('invoice.fields.invoiceFee') }} ({{ previewFeePercent }}%)</span>
    <span>-{{ formatCurrency(previewFeeAmount) }}</span>
  </div>
</div>
```

> 注：`/recharge` 路由名以本项目实际充值页路由为准（实现期确认，常见为 `/recharge` 或 `/billing`）。若 `common.recharge` 文案不存在，则在 zh/en 的 `common` 下补 `recharge: '去充值' / 'Top up'`。

- [ ] **Step 2.5: 确认充值路由 + common.recharge 文案存在**

Run: `cd frontend && grep -rn "path: '/recharge'\|name: 'recharge'\|recharge" src/router/ | head; grep -n "recharge:" src/i18n/locales/zh.ts | head`
Expected: 确认充值页路由路径;若不同则改上面的 `to`;若 `common.recharge` 缺失则补。

- [ ] **Step 3: 提交前二次确认 + 不足拦截**

把 `submitInvoiceRequest`（845 行）替换为带余额不足拦截 + 二次确认 + 选中档案完整性校验：

```ts
async function submitInvoiceRequest() {
  if (!selectedProfileId.value) {
    appStore.showError(t('invoice.messages.profileRequired'))
    return
  }
  const orderIds = Array.from(selectedOrderIds.value)
  if (orderIds.length === 0) {
    appStore.showError(t('invoice.messages.orderRequired'))
    return
  }
  // 选中档案缺专票必填信息 → 拦截
  if (selectedProfile.value && isProfileIncomplete(selectedProfile.value)) {
    appStore.showError(t('invoice.fee.incompleteProfile'))
    return
  }
  // 余额不足 → 拦截
  if (balanceInsufficient.value) {
    appStore.showError(t('invoice.fee.insufficient', {
      fee: formatCurrency(previewFeeAmount.value),
      balance: formatCurrency(userBalance.value),
      shortfall: formatCurrency(feeShortfall.value),
    }))
    return
  }
  // 有服务费 → 二次确认
  if (previewFeeAmount.value > 0) {
    const ok = window.confirm(t('invoice.fee.confirmBody', {
      rate: previewFeePercent.value,
      fee: formatCurrency(previewFeeAmount.value),
      balance: formatCurrency(userBalance.value),
      after: formatCurrency(previewBalanceAfter.value),
    }))
    if (!ok) return
  }

  actionLoading.value = true
  try {
    await invoiceAPI.createRequest({ profile_id: selectedProfileId.value, order_ids: orderIds })
    appStore.showSuccess(t('invoice.fee.deducted', { fee: formatCurrency(previewFeeAmount.value) }))
    clearSelection()
    activeTab.value = 'requests'
    requestPagination.page = 1
    await Promise.all([fetchRequests(), fetchInvoiceableOrders(), authStore.fetchProfile?.()])
  } catch (err: unknown) {
    showInvoiceError(err)
  } finally {
    actionLoading.value = false
  }
}
```

> `authStore.fetchProfile?.()` 用于刷新余额显示;若 auth store 的刷新方法名不同（实现期确认，常见 `fetchUser`/`refreshProfile`），改为对应方法或移除该项。

- [ ] **Step 3.5: 确认 auth store 刷新方法名**

Run: `cd frontend && grep -n "fetchProfile\|fetchUser\|refreshProfile\|async fetch" src/stores/auth.ts | head`
Expected: 确认刷新当前用户余额的方法名并据此修正上一步。

- [ ] **Step 4: 确保 RouterLink 已可用**

确认 `InvoiceView.vue` 顶部已 `import { RouterLink } from 'vue-router'` 或全局可用（Vue Router 全局注册 `RouterLink`，通常无需 import）。若 vue-tsc 报未定义则补 import。

- [ ] **Step 5: 类型检查 + 构建**

Run: `cd frontend && npx vue-tsc --noEmit -p tsconfig.json 2>&1 | grep -iE "InvoiceView|invoice" | head`
Expected: 无报错。

- [ ] **Step 6: 手工验证**

`cd frontend && npm run dev`，登录一个余额=0 的账号：选专票档案 + 勾订单 → 预览显示「需 ¥X、还差 ¥X」，提交被拦并提示充值。充值后余额>费用 → 提交弹 `confirm`，确认后成功 toast「已扣除 ¥X」，记录列表出现该申请，余额减少。

- [ ] **Step 7: Commit**

```bash
cd frontend && git add src/views/user/InvoiceView.vue
git commit -m "feat(invoice): balance preview, insufficient block, charge confirm dialog"
```

---

### Task B4: AdminInvoicesView 类型徽标简化

**Files:**
- Modify: `frontend/src/views/admin/AdminInvoicesView.vue:71-76`

- [ ] **Step 1: 简化徽标**

把 71-76 行的 `v-if="item.profile_snapshot?.invoice_type === 'vat_special'"` 徽标保留即可（现仅专票会命中）。为消除 TS 报错（`InvoiceType` 已收窄），把比较去掉或固定显示：

```vue
<span class="rounded-full bg-purple-50 px-2 py-0.5 text-xs font-medium text-purple-700 dark:bg-purple-900/30 dark:text-purple-300">
  {{ t('invoice.invoiceTypes.vat_special') }}
</span>
```

- [ ] **Step 2: 类型检查**

Run: `cd frontend && npx vue-tsc --noEmit -p tsconfig.json 2>&1 | grep -i admin | head`
Expected: 无 invoice 类型相关报错。

- [ ] **Step 3: Commit**

```bash
cd frontend && git add src/views/admin/AdminInvoicesView.vue
git commit -m "chore(invoice): simplify admin invoice-type badge (vat_special only)"
```

---

### Task B5: Phase A+B 集成验证 + 单测回归

- [ ] **Step 1: 后端测试**

Run: `cd backend && go test ./internal/service/ -run 'Invoice' -v && go build ./...`
Expected: PASS + 构建通过。

- [ ] **Step 2: 前端单测 + 构建**

Run: `cd frontend && npm run test:run && npm run build`
Expected: 既有测试通过、构建成功。

- [ ] **Step 3: 端到端手工核对（spec 验收项）**

- 新建档案缺银行字段被拒；普票选项已消失。
- 余额不足提交被拦（前端拦 + 后端 `INVOICE_BALANCE_INSUFFICIENT` 兜底）。
- 充值后提交：二次确认 → 扣费成功、`invoice_amount==base`、`fee_amount==base*rate`。
- 驳回/取消退回费用、幂等。

- [ ] **Step 4: 合并 Phase A+B（作为一个发布单元）**

按团队流程合并（A、B 同一 PR/同时合入）。

---

# Phase C：开票完成支持多附件

### Task C1: 后端 CompleteInvoiceRequest 接收多文件

**Files:**
- Modify: `backend/internal/service/invoice_admin_service.go:296-375`

- [ ] **Step 1: 入参改切片**

把 `CompleteInvoiceRequestInput`（296-300 行）改为：

```go
type CompleteInvoiceRequestInput struct {
	InvoiceNo string
	Files     []*multipart.FileHeader
}
```

- [ ] **Step 2: 校验与读取多文件、组装多附件**

把 `CompleteInvoiceRequest` 中单文件校验与读取段（317-372 行，从 `if input.File == nil {` 到构造 `att := EmailAttachment{...}` 并发送的部分）替换为多文件版本：

```go
	if len(input.Files) == 0 {
		return nil, infraerrors.BadRequest("INVOICE_FILE_REQUIRED", "invoice file is required")
	}
	if len(input.Files) > maxInvoiceAttachments {
		return nil, infraerrors.BadRequest("INVOICE_FILE_TOO_MANY", "too many invoice files")
	}

	if s.invoiceEmailSender == nil {
		return nil, infraerrors.ServiceUnavailable("INVOICE_EMAIL_NOT_CONFIGURED", "invoice email sender is not configured")
	}

	loaded, err := s.loadInvoiceRequest(ctx, 0, reqID)
	if err != nil {
		return nil, err
	}
	if loaded.Status != InvoiceStatusPending {
		return nil, infraerrors.Conflict("INVOICE_REQUEST_NOT_PENDING", "invoice request is not pending")
	}
	to := strings.TrimSpace(loaded.ProfileSnapshot.Email)
	if to == "" {
		return nil, infraerrors.BadRequest("INVOICE_EMAIL_REQUIRED", "profile email is required")
	}

	attachments, err := readInvoiceAttachments(input.Files)
	if err != nil {
		return nil, err
	}

	siteName := s.invoiceEmailSiteName(ctx)
	subject := fmt.Sprintf("[%s] 您的发票 %s / Your Invoice %s", siteName, invoiceNo, invoiceNo)
	body := buildInvoiceAttachmentEmailBody(loaded, invoiceNo, siteName)

	if err := s.invoiceEmailSender.SendEmailWithAttachment(ctx, to, subject, body, attachments); err != nil {
		return nil, infraerrors.ServiceUnavailable("INVOICE_EMAIL_SEND_FAILED", "failed to send invoice email").WithCause(err)
	}
```

- [ ] **Step 3: 新增常量与 readInvoiceAttachments 辅助**

在 `invoice_admin_service.go` 的 `maxInvoiceFileBytes` 常量附近加：

```go
const maxInvoiceAttachments = 5 // 单次开票最多附件数
```

并在文件末尾新增辅助函数（逐个按现有规则校验、读入内存、组装为 `[]EmailAttachment`）：

```go
// readInvoiceAttachments 校验并读取多个上传文件为邮件附件(逐个限大小与 MIME)。
func readInvoiceAttachments(files []*multipart.FileHeader) ([]EmailAttachment, error) {
	atts := make([]EmailAttachment, 0, len(files))
	for _, fh := range files {
		if fh == nil || fh.Size <= 0 {
			return nil, infraerrors.BadRequest("INVOICE_FILE_EMPTY", "invoice file is empty")
		}
		if fh.Size > maxInvoiceFileBytes {
			return nil, infraerrors.BadRequest("INVOICE_FILE_TOO_LARGE", "invoice file exceeds 10 MB")
		}
		mimeType := strings.TrimSpace(fh.Header.Get("Content-Type"))
		if mimeType != "" && !allowedInvoiceMimeTypes[mimeType] {
			return nil, infraerrors.BadRequest("INVOICE_FILE_TYPE_INVALID", "invoice file type is not allowed")
		}
		src, err := fh.Open()
		if err != nil {
			return nil, infraerrors.BadRequest("INVOICE_FILE_OPEN_FAILED", "failed to read uploaded file")
		}
		content, err := io.ReadAll(io.LimitReader(src, maxInvoiceFileBytes+1))
		_ = src.Close()
		if err != nil {
			return nil, infraerrors.InternalServer("INVOICE_FILE_READ_FAILED", "failed to read invoice file").WithCause(err)
		}
		if int64(len(content)) > maxInvoiceFileBytes {
			return nil, infraerrors.BadRequest("INVOICE_FILE_TOO_LARGE", "invoice file exceeds 10 MB")
		}
		filename := strings.TrimSpace(fh.Filename)
		if filename == "" {
			filename = "invoice.pdf"
		}
		atts = append(atts, EmailAttachment{Filename: filename, MimeType: mimeType, Content: content})
	}
	return atts, nil
}
```

- [ ] **Step 4: 编译**

Run: `cd backend && go build ./...`
Expected: 无错误（`io`、`multipart`、`strings` 已在该文件导入）。

- [ ] **Step 5: Commit**

```bash
cd backend && git add internal/service/invoice_admin_service.go
git commit -m "feat(invoice): complete invoice with multiple attachments"
```

---

### Task C2: 后端 handler 解析多文件

**Files:**
- Modify: `backend/internal/handler/admin/invoice_handler.go:121-149`

- [ ] **Step 1: 读取多文件表单**

把 `CompleteInvoiceRequest` handler 中读取单文件部分（132-142 行）替换为读取 `files`（多）并兼容旧单字段 `file`：

```go
	invoiceNo := strings.TrimSpace(c.PostForm("invoice_no"))
	form, err := c.MultipartForm()
	if err != nil {
		response.BadRequest(c, "Invoice file is required")
		return
	}
	files := form.File["files"]
	if len(files) == 0 {
		files = form.File["file"] // 向后兼容单文件字段
	}
	if len(files) == 0 {
		response.BadRequest(c, "Invoice file is required")
		return
	}

	req, err := h.paymentService.CompleteInvoiceRequest(c.Request.Context(), subject.UserID, id, service.CompleteInvoiceRequestInput{
		InvoiceNo: invoiceNo,
		Files:     files,
	})
```

- [ ] **Step 2: 编译**

Run: `cd backend && go build ./...`
Expected: 无错误。

- [ ] **Step 3: Commit**

```bash
cd backend && git add internal/handler/admin/invoice_handler.go
git commit -m "feat(invoice): admin handler accepts multiple invoice files"
```

---

### Task C3: 前端 admin 多选文件

**Files:**
- Modify: `frontend/src/api/admin/invoices.ts:22-26`
- Modify: `frontend/src/views/admin/AdminInvoicesView.vue`（complete 表单与提交）

- [ ] **Step 1: API 改多文件**

把 `complete` 改为接收 `File[]`：

```ts
  complete(id: number, invoiceNo: string, files: File[]) {
    const form = new FormData()
    form.append('invoice_no', invoiceNo)
    files.forEach((f) => form.append('files', f))
    return apiClient.post<InvoiceRequest>(`/admin/payment/invoices/${id}/complete`, form, {
      headers: { 'Content-Type': 'multipart/form-data' }
    })
  }
```

- [ ] **Step 2: complete 表单状态改数组**

在 `AdminInvoicesView.vue`：
- `completeForm.file: File | null` → `completeForm.files: File[]`，初始 `[]`；`openCompleteDialog` 里重置为 `completeForm.files = []`。
- 文件 input 加 `multiple`，`onFileChange` 收集多文件：

```ts
function onFileChange(event: Event) {
  const input = event.target as HTMLInputElement
  completeForm.files = input.files ? Array.from(input.files) : []
}
```

- 文件 input 模板加 `multiple`：

```vue
<input ref="fileInputRef" type="file" multiple class="hidden"
  accept=".pdf,.png,.jpg,.jpeg,.zip,.xls,.xlsx" @change="onFileChange" />
```

- 文件名展示改为列出多个：

```vue
<span v-if="completeForm.files.length" class="truncate text-sm text-gray-700 dark:text-gray-300">
  {{ completeForm.files.map(f => f.name).join('、') }}（{{ completeForm.files.length }}）
</span>
<span v-else class="text-sm text-gray-400">{{ t('common.noFileSelected') }}</span>
```

- `canSubmitComplete`（331 行）：`completeForm.files.length > 0`。
- `submitComplete`：`await adminInvoiceAPI.complete(activeRequest.value.id, invoiceNo, completeForm.files)`，开头判空改 `if (!activeRequest.value || completeForm.files.length === 0) return`。

- [ ] **Step 3: 类型检查 + 构建**

Run: `cd frontend && npx vue-tsc --noEmit -p tsconfig.json 2>&1 | grep -i admin | head && npm run build`
Expected: 无报错、构建成功。

- [ ] **Step 4: 手工验证**

管理员完成一条开票时选 2 个文件 → 客户邮箱收到含 2 个附件的一封邮件；申请状态变 completed。

- [ ] **Step 5: Commit**

```bash
cd frontend && git add src/api/admin/invoices.ts src/views/admin/AdminInvoicesView.vue
git commit -m "feat(invoice): admin complete dialog supports multiple files"
```

---

# Phase D：对公直发发票邮件（多附件 + 记录）

### Task D1: 迁移 147（直发记录表）

**Files:**
- Create: `backend/migrations/147_invoice_email_sends.sql`

- [ ] **Step 1: 写迁移**

```sql
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
```

- [ ] **Step 2: Commit**

```bash
cd backend && git add migrations/147_invoice_email_sends.sql
git commit -m "feat(invoice): migration 147 - invoice_email_sends table"
```

---

### Task D2: 后端 SendInvoiceEmail 服务

**Files:**
- Create: `backend/internal/service/invoice_direct_send.go`

- [ ] **Step 1: 写服务方法**

```go
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
```

- [ ] **Step 2: 编译**

Run: `cd backend && go build ./...`
Expected: 无错误。

- [ ] **Step 3: Commit**

```bash
cd backend && git add internal/service/invoice_direct_send.go
git commit -m "feat(invoice): SendInvoiceEmail direct send with record"
```

---

### Task D3: 后端 handler + 路由

**Files:**
- Modify: `backend/internal/handler/admin/invoice_handler.go`（新 handler 方法）
- Modify: `backend/internal/server/routes/payment.go:101-105`

- [ ] **Step 1: 新 handler 方法**

在 `admin/invoice_handler.go` 末尾（`sanitizeHeader` 之前或文件尾）新增：

```go
// SendInvoiceEmail directly emails uploaded invoice files to a recipient (对公直发).
// POST /api/v1/admin/payment/invoices/send-email
// Multipart fields: recipient_email, subject(optional), note(optional), files[] (1..N)
func (h *InvoiceHandler) SendInvoiceEmail(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "unauthorized")
		return
	}
	form, err := c.MultipartForm()
	if err != nil {
		response.BadRequest(c, "files are required")
		return
	}
	files := form.File["files"]
	if len(files) == 0 {
		response.BadRequest(c, "at least one attachment is required")
		return
	}
	err = h.paymentService.SendInvoiceEmail(c.Request.Context(), subject.UserID, service.SendInvoiceEmailInput{
		RecipientEmail: strings.TrimSpace(c.PostForm("recipient_email")),
		Subject:        strings.TrimSpace(c.PostForm("subject")),
		Note:           strings.TrimSpace(c.PostForm("note")),
		Files:          files,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "invoice email sent"})
}
```

- [ ] **Step 2: 注册路由**

在 `backend/internal/server/routes/payment.go` 的 `adminInvoices` 块（99-105 行）中加一行：

```go
			adminInvoices.POST("/:id/complete", adminInvoiceHandler.CompleteInvoiceRequest)
			adminInvoices.POST("/:id/reject", adminInvoiceHandler.RejectInvoiceRequest)
			adminInvoices.POST("/send-email", adminInvoiceHandler.SendInvoiceEmail)
```

> 注：`send-email` 与 `/:id` 同级；gin 中静态段优先于通配，但为稳妥放在 `:id` 路由之后注册即可（gin 的 path 段 `send-email` 不会与 `:id` 冲突，因为 `:id` 仅匹配单段且 `send-email` 是字面量段）。

- [ ] **Step 3: 编译**

Run: `cd backend && go build ./...`
Expected: 无错误。

- [ ] **Step 4: 手工验证**

```bash
curl -X POST "$BASE/api/v1/admin/payment/invoices/send-email" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -F "recipient_email=test@example.com" \
  -F "note=对公打款发票" \
  -F "files=@/path/a.pdf" -F "files=@/path/b.pdf"
```
Expected: 200;收件箱收到含 2 附件的邮件;`SELECT * FROM invoice_email_sends ORDER BY id DESC LIMIT 1` 有一行 `status=sent`、`attachment_count=2`。

- [ ] **Step 5: Commit**

```bash
cd backend && git add internal/handler/admin/invoice_handler.go internal/server/routes/payment.go
git commit -m "feat(invoice): admin route + handler for direct invoice email"
```

---

### Task D4: 前端对公直发表单 + 历史

**Files:**
- Modify: `frontend/src/api/admin/invoices.ts`（新增 sendEmail + listEmailSends）
- Modify: `frontend/src/views/admin/AdminInvoicesView.vue`（直发表单 + 历史列表）
- Modify: `frontend/src/types/invoice.ts`（新增 InvoiceEmailSend 类型）

- [ ] **Step 1: 类型**

在 `frontend/src/types/invoice.ts` 末尾新增：

```ts
export interface InvoiceEmailSend {
  id: number
  recipient_email: string
  subject: string
  note?: string | null
  attachment_count: number
  attachment_names?: string[] | null
  status: 'sent' | 'failed'
  error_message?: string | null
  sent_by: number
  sent_at: string
}
```

- [ ] **Step 2: API**

在 `frontend/src/api/admin/invoices.ts` 的 `adminInvoiceAPI` 中新增：

```ts
  sendEmail(recipientEmail: string, files: File[], subject?: string, note?: string) {
    const form = new FormData()
    form.append('recipient_email', recipientEmail)
    if (subject) form.append('subject', subject)
    if (note) form.append('note', note)
    files.forEach((f) => form.append('files', f))
    return apiClient.post<{ message: string }>(`/admin/payment/invoices/send-email`, form, {
      headers: { 'Content-Type': 'multipart/form-data' }
    })
  }
```

> 历史列表读取需要一个 GET 接口。本计划后端未实现 `list email sends` 的 GET（spec 要求“留记录”，查看可后续加）。**实现期决策**：如需在 UI 看历史，追加一个 `GET /admin/payment/invoices/email-sends`（service 加 `ListInvoiceEmailSends` 分页查询 + handler + 路由 + 这里的 `listEmailSends`）。否则本任务仅实现“发送”，历史用 DB 查询/后续补 UI。下面 Step 3 仅实现发送表单。

- [ ] **Step 3: 直发表单 UI + 提交**

在 `AdminInvoicesView.vue` 顶部工具栏加一个「直接发送发票」按钮，点击打开一个对话框（与 complete 对话框同模式），包含：收件邮箱（必填）、主题（选填）、备注（选填）、多文件选择（必填）。提交：

```ts
const sendForm = reactive<{ email: string; subject: string; note: string; files: File[] }>({
  email: '', subject: '', note: '', files: []
})
const sendDialogOpen = ref(false)

function onSendFileChange(event: Event) {
  const input = event.target as HTMLInputElement
  sendForm.files = input.files ? Array.from(input.files) : []
}

async function submitSendEmail() {
  if (!sendForm.email.trim()) { appStore.showError(t('invoice.admin.recipientRequired')); return }
  if (sendForm.files.length === 0) { appStore.showError(t('common.noFileSelected')); return }
  actionLoading.value = true
  try {
    await adminInvoiceAPI.sendEmail(sendForm.email.trim(), sendForm.files, sendForm.subject.trim(), sendForm.note.trim())
    appStore.showSuccess(t('invoice.admin.emailSent'))
    sendDialogOpen.value = false
    sendForm.email = ''; sendForm.subject = ''; sendForm.note = ''; sendForm.files = []
  } catch (err: unknown) {
    showError(err)
  } finally {
    actionLoading.value = false
  }
}
```

对话框模板（放在 complete 对话框附近）：

```vue
<BaseDialog v-model="sendDialogOpen" :title="t('invoice.admin.directSendTitle')">
  <form class="space-y-4" @submit.prevent="submitSendEmail">
    <div>
      <label class="input-label">{{ t('invoice.admin.recipientEmail') }} <span class="text-red-500">*</span></label>
      <input v-model="sendForm.email" type="email" class="input mt-1 w-full" required />
    </div>
    <div>
      <label class="input-label">{{ t('invoice.admin.subjectOptional') }}</label>
      <input v-model="sendForm.subject" type="text" class="input mt-1 w-full" maxlength="200" />
    </div>
    <div>
      <label class="input-label">{{ t('invoice.admin.noteOptional') }}</label>
      <textarea v-model="sendForm.note" class="input mt-1 w-full" rows="2" maxlength="500"></textarea>
    </div>
    <div>
      <label class="input-label">{{ t('invoice.admin.fileLabel') }} <span class="text-red-500">*</span></label>
      <input type="file" multiple class="mt-1" accept=".pdf,.png,.jpg,.jpeg,.zip,.xls,.xlsx" @change="onSendFileChange" />
      <span v-if="sendForm.files.length" class="ml-2 text-sm text-gray-600">{{ sendForm.files.map(f => f.name).join('、') }}</span>
    </div>
    <div class="flex justify-end gap-2">
      <button type="button" class="btn btn-secondary" @click="sendDialogOpen = false">{{ t('common.cancel') }}</button>
      <button type="submit" class="btn btn-primary" :disabled="actionLoading">{{ t('invoice.admin.sendBtn') }}</button>
    </div>
  </form>
</BaseDialog>
```

> `BaseDialog`/按钮组件名以本视图已用的对话框组件为准（实现期对齐 complete 对话框所用组件）。

- [ ] **Step 4: i18n 文案**

在 zh.ts / en.ts 的 `invoice.admin` 下补：`directSendTitle`、`recipientEmail`、`recipientRequired`、`subjectOptional`、`noteOptional`、`sendBtn`、`emailSent`。例如 zh：

```js
      directSendTitle: '直接发送发票（对公）',
      recipientEmail: '收件邮箱',
      recipientRequired: '请填写收件邮箱',
      subjectOptional: '主题（选填）',
      noteOptional: '备注（选填）',
      sendBtn: '发送',
      emailSent: '发票邮件已发送',
```

en：

```js
      directSendTitle: 'Send Invoice Directly',
      recipientEmail: 'Recipient Email',
      recipientRequired: 'Recipient email is required',
      subjectOptional: 'Subject (optional)',
      noteOptional: 'Note (optional)',
      sendBtn: 'Send',
      emailSent: 'Invoice email sent',
```

- [ ] **Step 5: 类型检查 + 构建**

Run: `cd frontend && npx vue-tsc --noEmit -p tsconfig.json 2>&1 | grep -i admin | head && npm run build`
Expected: 无报错、构建成功。

- [ ] **Step 6: 手工验证**

管理员点「直接发送发票」→ 填邮箱 + 选 2 文件 + 发送 → 收件箱收到含 2 附件邮件;DB `invoice_email_sends` 新增一行 `status=sent`。

- [ ] **Step 7: Commit**

```bash
cd frontend && git add src/api/admin/invoices.ts src/views/admin/AdminInvoicesView.vue src/types/invoice.ts src/i18n/locales/zh.ts src/i18n/locales/en.ts
git commit -m "feat(invoice): admin direct-send invoice email form"
```

---

## 自检（Self-Review 已执行）

- **Spec 覆盖**：Part1=Task A1/A2/A3/A7/A8 + B3；Part2=Task D1-D4；Part3=Task C1-C3；Part4=Task A1/A6 + B1/B2/B4。缓存失效=A4/A5。✅
- **类型一致**：`CompleteInvoiceRequestInput.Files`（C1）与 handler（C2）一致；`SendInvoiceEmailInput.Files`（D2）与 handler（D3）一致；`invoiceFeeShortfall`（A3）签名与 A7 调用一致；`InvalidateBalanceCaches`（A4）与 A7/A8 调用一致；前端 `complete(id, no, files: File[])`（C3）与 `sendEmail(...)`（D4）一致。✅
- **占位符**：无 TBD/“类似上文”。少数“实现期确认”项（充值路由名、auth store 刷新方法名、对话框组件名、是否加历史 GET 接口）已显式标注为需在实现时用一条 grep 命令确认，并给了确认步骤，非占位。✅

## 执行选择

Plan complete and saved to `docs/superpowers/plans/2026-06-01-invoice-fee-balance-and-direct-send.md`. Two execution options:

1. **Subagent-Driven (recommended)** — 每个任务派发独立子代理，任务间审查，迭代快。
2. **Inline Execution** — 本会话内分批执行 + 检查点审查。

Which approach?
