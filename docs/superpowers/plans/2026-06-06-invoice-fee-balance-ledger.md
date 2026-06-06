# 发票服务费余额流水账本 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用户提交发票被扣 6% 服务费（及驳回/取消退费）时，向 `redeem_codes` 账本写一条 `invoice_fee` 记录，使其自动出现在管理员兑换码页与用户 `/redeem` 余额变动历史中。

**Architecture:** 复用既有 `redeem_codes` 余额流水账本（与 `admin_balance` 手动调整记录同机制）。在发票扣费/退费的**现有 `database/sql` 事务内**用原始 SQL 写一条账本行（扣费 `value<0`、退费 `value>0`，`status='used'`，`used_by=用户`，`notes` 含发票流水号），保证账本与余额强一致。展示侧两处账本视图（管理员兑换码页、用户 `/redeem`）后端不过滤类型，记录自动出现；仅需补前端类型渲染分支与中英文 i18n。

**Tech Stack:** Go（`database/sql` 原始事务，ent 仅用于读侧查询）、PostgreSQL（`redeem_codes` 表，无新迁移）、Vue 3 + TypeScript、vue-i18n、测试用 `github.com/DATA-DOG/go-sqlmock`（后端）与 `vue-tsc`（前端类型检查）。

参考规格：[`docs/superpowers/specs/2026-06-06-invoice-fee-balance-ledger-design.md`](../specs/2026-06-06-invoice-fee-balance-ledger-design.md)

---

## File Structure

**后端（无新迁移）**
- `backend/internal/service/domain_constants.go` — 新增 `RedeemTypeInvoiceFee = "invoice_fee"` 常量。
- `backend/internal/service/invoice_ledger.go` — **新建**：账本条目结构 + 两个纯构造函数 + 事务内 INSERT helper。
- `backend/internal/service/invoice_ledger_test.go` — **新建**：纯构造函数单测 + sqlmock INSERT 单测。
- `backend/internal/service/invoice_service.go` — `CreateInvoiceRequest`：扣费成功后、提交前写扣费账本行。
- `backend/internal/service/invoice_admin_service.go` — `RejectInvoiceRequest` / `CancelInvoiceRequest`：退费分支写退费账本行（并在 SELECT 取 `serial_no`）。
- `backend/internal/repository/redeem_code_repo.go` — `SumPositiveBalanceByUser`：仅加注释（白名单已天然排除 `invoice_fee`，**不改逻辑**）。

**前端**
- `frontend/src/views/admin/RedeemView.vue` — 筛选下拉新增 `invoice_fee` 项；value 单元格新增货币格式分支。
- `frontend/src/views/user/RedeemView.vue` — `isBalanceType` / `getHistoryItemTitle` 新增 `invoice_fee` 分支。
- `frontend/src/i18n/locales/zh.ts` / `en.ts` — 新增 `admin.redeem.types.invoice_fee` 与 `redeem.invoiceFeeCharged` / `redeem.invoiceFeeRefunded`。

**说明：** 本仓库 service 层对 raw-SQL 路径无 DB 集成测试（仅对纯函数做单测，如 `invoice_fee_test.go`）。本计划据此把可测逻辑（金额正负、备注、INSERT 语句与参数）抽到可单测的 helper 用纯函数 + sqlmock 覆盖；三处事务接线（Task 4/5/6）为一行式调用，由 `go build` + `go vet` + 既有测试 + 人工/CI 验证。前端视图组件本仓库无单测（仅 utils/api 有），故前端改动以 `vue-tsc` 类型检查 + i18n 键存在性校验验证。

---

### Task 1: 新增 `invoice_fee` 常量与账本 helper（TDD）

**Files:**
- Modify: `backend/internal/service/domain_constants.go:76-83`
- Create: `backend/internal/service/invoice_ledger.go`
- Test: `backend/internal/service/invoice_ledger_test.go`

- [ ] **Step 1: 写失败测试（纯构造函数 + sqlmock INSERT）**

Create `backend/internal/service/invoice_ledger_test.go`:

```go
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
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd backend && go test ./internal/service/ -run 'InvoiceFeeLedger|InsertInvoiceFeeLedger' -count=1`
Expected: 编译失败 —— `undefined: newInvoiceFeeChargeEntry` / `newInvoiceFeeRefundEntry` / `insertInvoiceFeeLedgerTx` / `RedeemTypeInvoiceFee`。

- [ ] **Step 3: 新增常量**

In `backend/internal/service/domain_constants.go`, change the redeem-type const block (lines 76-83):

Old:
```go
// Redeem type constants
const (
	RedeemTypeBalance          = domain.RedeemTypeBalance
	RedeemTypeConcurrency      = domain.RedeemTypeConcurrency
	RedeemTypeSubscription     = domain.RedeemTypeSubscription
	RedeemTypeInvitation       = domain.RedeemTypeInvitation
	RedeemTypeAffiliateBalance = "affiliate_balance"
)
```

New:
```go
// Redeem type constants
const (
	RedeemTypeBalance          = domain.RedeemTypeBalance
	RedeemTypeConcurrency      = domain.RedeemTypeConcurrency
	RedeemTypeSubscription     = domain.RedeemTypeSubscription
	RedeemTypeInvitation       = domain.RedeemTypeInvitation
	RedeemTypeAffiliateBalance = "affiliate_balance"
	RedeemTypeInvoiceFee       = "invoice_fee" // 开票服务费余额流水（提交扣费为负、驳回/取消退费为正）
)
```

- [ ] **Step 4: 实现 helper 文件**

Create `backend/internal/service/invoice_ledger.go`:

```go
package service

import (
	"context"
	"database/sql"
	"fmt"
)

// invoiceFeeLedgerEntry is one invoice-fee balance-ledger row.
// Value<0 = charge (deduction at submit); Value>0 = refund (reject/cancel).
type invoiceFeeLedgerEntry struct {
	UserID int64
	Value  float64
	Note   string
}

// newInvoiceFeeChargeEntry builds the ledger entry for charging the invoice fee.
func newInvoiceFeeChargeEntry(userID int64, feeAmount float64, serialNo string) invoiceFeeLedgerEntry {
	return invoiceFeeLedgerEntry{
		UserID: userID,
		Value:  -feeAmount,
		Note:   fmt.Sprintf("发票服务费 · 申请 %s", serialNo),
	}
}

// newInvoiceFeeRefundEntry builds the ledger entry for refunding the invoice fee.
func newInvoiceFeeRefundEntry(userID int64, feeAmount float64, serialNo string) invoiceFeeLedgerEntry {
	return invoiceFeeLedgerEntry{
		UserID: userID,
		Value:  feeAmount,
		Note:   fmt.Sprintf("发票服务费退回 · 申请 %s", serialNo),
	}
}

// insertInvoiceFeeLedgerTx writes one invoice-fee ledger row into redeem_codes
// within the caller's transaction, mirroring the admin_balance adjustment record
// created by adminServiceImpl.UpdateUserBalance. The synthetic code is generated
// the same way; status is 'used' so it can never be redeemed as a code.
func insertInvoiceFeeLedgerTx(ctx context.Context, tx *sql.Tx, e invoiceFeeLedgerEntry) error {
	code, err := GenerateRedeemCode()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO redeem_codes (code, type, value, status, used_by, used_at, notes)
		VALUES ($1, $2, $3, 'used', $4, NOW(), $5)
	`, code, RedeemTypeInvoiceFee, e.Value, e.UserID, e.Note)
	return err
}
```

- [ ] **Step 5: 运行测试确认通过**

Run: `cd backend && gofmt -w internal/service/invoice_ledger.go internal/service/invoice_ledger_test.go internal/service/domain_constants.go && go test ./internal/service/ -run 'InvoiceFeeLedger|InsertInvoiceFeeLedger' -count=1`
Expected: `ok  github.com/Wei-Shaw/sub2api/internal/service` —— 两个测试 PASS。

- [ ] **Step 6: 提交**

```bash
cd /Users/zhengshan/projects/sub2api
git add backend/internal/service/domain_constants.go backend/internal/service/invoice_ledger.go backend/internal/service/invoice_ledger_test.go
git commit -m "feat(invoice): invoice_fee ledger helper + redeem type constant"
```

---

### Task 2: 提交开票时写扣费账本行（`CreateInvoiceRequest`）

**Files:**
- Modify: `backend/internal/service/invoice_service.go:528-531`

`serialNo`（line 510）与 `userID`、`feeAmount` 在事务内均已就绪。账本行需在提交（`tx.Commit()`）前、订单关联循环之后写入。

- [ ] **Step 1: 在 `tx.Commit()` 前插入扣费账本**

In `backend/internal/service/invoice_service.go`, locate the end of `CreateInvoiceRequest`. 

Old:
```go
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
```

New:
```go
	for _, order := range orders {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO invoice_request_orders (invoice_request_id, payment_order_id)
			VALUES ($1, $2)
		`, req.ID, order.ID); err != nil {
			return nil, infraerrors.InternalServer("INVOICE_REQUEST_CREATE_FAILED", "failed to attach invoice order").WithCause(err)
		}
	}
	// 记录开票服务费余额流水（与扣费同事务，保证账本与余额强一致）。
	if feeAmount > 0 {
		if err := insertInvoiceFeeLedgerTx(ctx, tx, newInvoiceFeeChargeEntry(userID, feeAmount, serialNo)); err != nil {
			return nil, infraerrors.InternalServer("INVOICE_REQUEST_CREATE_FAILED", "failed to record invoice fee ledger").WithCause(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, infraerrors.InternalServer("INVOICE_REQUEST_CREATE_FAILED", "failed to commit invoice request").WithCause(err)
	}
```

- [ ] **Step 2: 编译 + vet 确认通过**

Run: `cd backend && gofmt -w internal/service/invoice_service.go && go build ./... && go vet ./internal/service/`
Expected: 无输出，退出码 0。

- [ ] **Step 3: 跑发票相关测试确认未回归**

Run: `cd backend && go test ./internal/service/ -run 'Invoice' -count=1`
Expected: `ok  github.com/Wei-Shaw/sub2api/internal/service`。

- [ ] **Step 4: 提交**

```bash
cd /Users/zhengshan/projects/sub2api
git add backend/internal/service/invoice_service.go
git commit -m "feat(invoice): record fee deduction to balance ledger on submit"
```

---

### Task 3: 驳回退费时写退费账本行（`RejectInvoiceRequest`）

**Files:**
- Modify: `backend/internal/service/invoice_admin_service.go:288-309`

需在 SELECT 中补 `serial_no` 并 scan，退费分支内写正数账本行。

- [ ] **Step 1: SELECT 增加 `serial_no`**

In `RejectInvoiceRequest`, change the lock-and-read block.

Old:
```go
	// 锁定并读取扣费状态
	var userID int64
	var feeAmount float64
	var status string
	var feeChargedAt, feeRefundedAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `
		SELECT user_id, status, fee_amount::float8, fee_charged_at, fee_refunded_at
		FROM invoice_requests WHERE id = $1 FOR UPDATE
	`, reqID).Scan(&userID, &status, &feeAmount, &feeChargedAt, &feeRefundedAt); err != nil {
```

New:
```go
	// 锁定并读取扣费状态
	var userID int64
	var feeAmount float64
	var status, serialNo string
	var feeChargedAt, feeRefundedAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `
		SELECT user_id, status, serial_no, fee_amount::float8, fee_charged_at, fee_refunded_at
		FROM invoice_requests WHERE id = $1 FOR UPDATE
	`, reqID).Scan(&userID, &status, &serialNo, &feeAmount, &feeChargedAt, &feeRefundedAt); err != nil {
```

- [ ] **Step 2: 退费分支写账本**

Old:
```go
	if feeAmount > 0 && feeChargedAt.Valid && !feeRefundedAt.Valid {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET balance = balance + $1 WHERE id = $2`, feeAmount, userID); err != nil {
			return nil, infraerrors.InternalServer("INVOICE_REQUEST_REJECT_FAILED", "failed to refund invoice fee").WithCause(err)
		}
	}
```

New:
```go
	if feeAmount > 0 && feeChargedAt.Valid && !feeRefundedAt.Valid {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET balance = balance + $1 WHERE id = $2`, feeAmount, userID); err != nil {
			return nil, infraerrors.InternalServer("INVOICE_REQUEST_REJECT_FAILED", "failed to refund invoice fee").WithCause(err)
		}
		if err := insertInvoiceFeeLedgerTx(ctx, tx, newInvoiceFeeRefundEntry(userID, feeAmount, serialNo)); err != nil {
			return nil, infraerrors.InternalServer("INVOICE_REQUEST_REJECT_FAILED", "failed to record invoice fee refund ledger").WithCause(err)
		}
	}
```

- [ ] **Step 3: 编译 + vet**

Run: `cd backend && gofmt -w internal/service/invoice_admin_service.go && go build ./... && go vet ./internal/service/`
Expected: 无输出，退出码 0。

- [ ] **Step 4: 提交**

```bash
cd /Users/zhengshan/projects/sub2api
git add backend/internal/service/invoice_admin_service.go
git commit -m "feat(invoice): record fee refund to balance ledger on reject"
```

---

### Task 4: 取消退费时写退费账本行（`CancelInvoiceRequest`）

**Files:**
- Modify: `backend/internal/service/invoice_admin_service.go:228-249`

`userID` 为函数入参，已可用；需在 SELECT 中补 `serial_no`，退费分支内写正数账本行。

- [ ] **Step 1: SELECT 增加 `serial_no`**

In `CancelInvoiceRequest`, change the lock-and-read block.

Old:
```go
	var status string
	var feeAmount float64
	var feeChargedAt, feeRefundedAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `
		SELECT status, fee_amount::float8, fee_charged_at, fee_refunded_at
		FROM invoice_requests
		WHERE id = $1 AND user_id = $2
		FOR UPDATE
	`, reqID, userID).Scan(&status, &feeAmount, &feeChargedAt, &feeRefundedAt); err != nil {
```

New:
```go
	var status, serialNo string
	var feeAmount float64
	var feeChargedAt, feeRefundedAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `
		SELECT status, serial_no, fee_amount::float8, fee_charged_at, fee_refunded_at
		FROM invoice_requests
		WHERE id = $1 AND user_id = $2
		FOR UPDATE
	`, reqID, userID).Scan(&status, &serialNo, &feeAmount, &feeChargedAt, &feeRefundedAt); err != nil {
```

- [ ] **Step 2: 退费分支写账本**

Old:
```go
	if feeAmount > 0 && feeChargedAt.Valid && !feeRefundedAt.Valid {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET balance = balance + $1 WHERE id = $2`, feeAmount, userID); err != nil {
			return infraerrors.InternalServer("INVOICE_REQUEST_CANCEL_FAILED", "failed to refund invoice fee").WithCause(err)
		}
	}
```

New:
```go
	if feeAmount > 0 && feeChargedAt.Valid && !feeRefundedAt.Valid {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET balance = balance + $1 WHERE id = $2`, feeAmount, userID); err != nil {
			return infraerrors.InternalServer("INVOICE_REQUEST_CANCEL_FAILED", "failed to refund invoice fee").WithCause(err)
		}
		if err := insertInvoiceFeeLedgerTx(ctx, tx, newInvoiceFeeRefundEntry(userID, feeAmount, serialNo)); err != nil {
			return infraerrors.InternalServer("INVOICE_REQUEST_CANCEL_FAILED", "failed to record invoice fee refund ledger").WithCause(err)
		}
	}
```

- [ ] **Step 3: 编译 + vet + 测试**

Run: `cd backend && gofmt -w internal/service/invoice_admin_service.go && go build ./... && go vet ./internal/service/ && go test ./internal/service/ -run 'Invoice' -count=1`
Expected: 无构建/vet 输出；测试 `ok  github.com/Wei-Shaw/sub2api/internal/service`。

- [ ] **Step 4: 提交**

```bash
cd /Users/zhengshan/projects/sub2api
git add backend/internal/service/invoice_admin_service.go
git commit -m "feat(invoice): record fee refund to balance ledger on cancel"
```

---

### Task 5: 在 `SumPositiveBalanceByUser` 加防误改注释（无逻辑改动）

**Files:**
- Modify: `backend/internal/repository/redeem_code_repo.go:389-399`

`SumPositiveBalanceByUser` 已用 `TypeIn("balance","admin_balance")` 白名单，`invoice_fee` 天然不计入"累计充值"。仅加注释，避免后人误把 `invoice_fee` 加进白名单（那会让退费正数行虚增累计充值）。

- [ ] **Step 1: 加注释**

Old:
```go
// SumPositiveBalanceByUser returns total recharged amount (sum of value > 0 where type is balance/admin_balance).
func (r *redeemCodeRepository) SumPositiveBalanceByUser(ctx context.Context, userID int64) (float64, error) {
	var result []struct {
		Sum float64 `json:"sum"`
	}
	err := r.client.RedeemCode.Query().
		Where(
			redeemcode.UsedByEQ(userID),
			redeemcode.ValueGT(0),
			redeemcode.TypeIn("balance", "admin_balance"),
		).
```

New:
```go
// SumPositiveBalanceByUser returns total recharged amount (sum of value > 0 where type is balance/admin_balance).
//
// NOTE: the type whitelist below intentionally excludes "invoice_fee". Invoice-fee
// refunds are positive ledger rows but are NOT recharges — do NOT add "invoice_fee"
// here, or refunds would inflate the user's total recharged figure.
func (r *redeemCodeRepository) SumPositiveBalanceByUser(ctx context.Context, userID int64) (float64, error) {
	var result []struct {
		Sum float64 `json:"sum"`
	}
	err := r.client.RedeemCode.Query().
		Where(
			redeemcode.UsedByEQ(userID),
			redeemcode.ValueGT(0),
			redeemcode.TypeIn("balance", "admin_balance"),
		).
```

- [ ] **Step 2: 编译**

Run: `cd backend && gofmt -w internal/repository/redeem_code_repo.go && go build ./...`
Expected: 无输出，退出码 0。

- [ ] **Step 3: 提交**

```bash
cd /Users/zhengshan/projects/sub2api
git add backend/internal/repository/redeem_code_repo.go
git commit -m "docs(invoice): note invoice_fee is excluded from total_recharged"
```

---

### Task 6: 前端管理员兑换码页显示 `invoice_fee`（筛选 + 金额 + i18n）

**Files:**
- Modify: `frontend/src/views/admin/RedeemView.vue:131-141`（value 单元格）、`:741-747`（筛选项）
- Modify: `frontend/src/i18n/locales/zh.ts:4424-4432`、`frontend/src/i18n/locales/en.ts:4359-4367`

- [ ] **Step 1: 筛选下拉新增 invoice_fee**

In `frontend/src/views/admin/RedeemView.vue`, change `filterTypeOptions` (lines 741-747).

Old:
```js
const filterTypeOptions = computed(() => [
  { value: '', label: t('admin.redeem.allTypes') },
  { value: 'balance', label: t('admin.redeem.balance') },
  { value: 'concurrency', label: t('admin.redeem.concurrency') },
  { value: 'subscription', label: t('admin.redeem.subscription') },
  { value: 'invitation', label: t('admin.redeem.invitation') }
])
```

New:
```js
const filterTypeOptions = computed(() => [
  { value: '', label: t('admin.redeem.allTypes') },
  { value: 'balance', label: t('admin.redeem.balance') },
  { value: 'concurrency', label: t('admin.redeem.concurrency') },
  { value: 'subscription', label: t('admin.redeem.subscription') },
  { value: 'invitation', label: t('admin.redeem.invitation') },
  { value: 'invoice_fee', label: t('admin.redeem.types.invoice_fee') }
])
```

- [ ] **Step 2: value 单元格新增货币格式分支**

Change the `#cell-value` template (lines 131-142).

Old:
```html
          <template #cell-value="{ value, row }">
            <span class="text-sm font-medium text-gray-900 dark:text-white">
              <template v-if="row.type === 'balance'">${{ value.toFixed(2) }}</template>
              <template v-else-if="row.type === 'subscription'">
                {{ row.validity_days || 30 }} {{ t('admin.redeem.days') }}
                <span v-if="row.group" class="ml-1 text-xs text-gray-500 dark:text-gray-400"
                  >({{ row.group.name }})</span
                >
              </template>
              <template v-else>{{ value }}</template>
            </span>
          </template>
```

New:
```html
          <template #cell-value="{ value, row }">
            <span class="text-sm font-medium text-gray-900 dark:text-white">
              <template v-if="row.type === 'balance'">${{ value.toFixed(2) }}</template>
              <template v-else-if="row.type === 'invoice_fee'">${{ value.toFixed(2) }}</template>
              <template v-else-if="row.type === 'subscription'">
                {{ row.validity_days || 30 }} {{ t('admin.redeem.days') }}
                <span v-if="row.group" class="ml-1 text-xs text-gray-500 dark:text-gray-400"
                  >({{ row.group.name }})</span
                >
              </template>
              <template v-else>{{ value }}</template>
            </span>
          </template>
```

- [ ] **Step 3: i18n（zh）新增类型标签**

In `frontend/src/i18n/locales/zh.ts`, change the `types` block (lines 4424-4432).

Old:
```js
      types: {
        balance: '余额',
        concurrency: '并发数',
        subscription: '订阅',
        invitation: '邀请码',
        // 管理员在用户管理页面调整余额/并发时产生的记录
        admin_balance: '余额（管理员）',
        admin_concurrency: '并发数（管理员）'
      },
```

New:
```js
      types: {
        balance: '余额',
        concurrency: '并发数',
        subscription: '订阅',
        invitation: '邀请码',
        // 管理员在用户管理页面调整余额/并发时产生的记录
        admin_balance: '余额（管理员）',
        admin_concurrency: '并发数（管理员）',
        // 提交/驳回/取消发票时的开票服务费余额流水
        invoice_fee: '发票服务费'
      },
```

- [ ] **Step 4: i18n（en）新增类型标签**

In `frontend/src/i18n/locales/en.ts`, change the `types` block (lines 4359-4367).

Old:
```js
      types: {
        balance: 'Balance',
        concurrency: 'Concurrency',
        subscription: 'Subscription',
        invitation: 'Invitation',
        // Admin adjustment types (created when admin modifies user balance/concurrency)
        admin_balance: 'Balance (Admin)',
        admin_concurrency: 'Concurrency (Admin)'
      },
```

New:
```js
      types: {
        balance: 'Balance',
        concurrency: 'Concurrency',
        subscription: 'Subscription',
        invitation: 'Invitation',
        // Admin adjustment types (created when admin modifies user balance/concurrency)
        admin_balance: 'Balance (Admin)',
        admin_concurrency: 'Concurrency (Admin)',
        // Invoice service fee ledger (charged on submit, refunded on reject/cancel)
        invoice_fee: 'Invoice Fee'
      },
```

- [ ] **Step 5: 类型检查 + i18n 键校验**

Run:
```bash
cd frontend && npx vue-tsc --noEmit && \
grep -q "invoice_fee: '发票服务费'" src/i18n/locales/zh.ts && \
grep -q "invoice_fee: 'Invoice Fee'" src/i18n/locales/en.ts && echo OK
```
Expected: 类型检查无报错；末尾打印 `OK`。

- [ ] **Step 6: 提交**

```bash
cd /Users/zhengshan/projects/sub2api
git add frontend/src/views/admin/RedeemView.vue frontend/src/i18n/locales/zh.ts frontend/src/i18n/locales/en.ts
git commit -m "feat(invoice): show invoice_fee in admin redeem-code page"
```

---

### Task 7: 前端用户 `/redeem` 显示 `invoice_fee`（标题/格式 + i18n）

**Files:**
- Modify: `frontend/src/views/user/RedeemView.vue:381-406`
- Modify: `frontend/src/i18n/locales/zh.ts:1164`、`frontend/src/i18n/locales/en.ts:1160`

`invoice_fee` 归为余额类（`isBalanceType`）后，金额会按 `formatHistoryValue` 的余额分支显示为 `$x.xx`（line 408-411），无需改 `formatHistoryValue`。仅需 `isBalanceType` + `getHistoryItemTitle`。

- [ ] **Step 1: `isBalanceType` 纳入 invoice_fee**

In `frontend/src/views/user/RedeemView.vue`, change `isBalanceType` (lines 381-383).

Old:
```js
const isBalanceType = (type: string) => {
  return type === 'balance' || type === 'admin_balance'
}
```

New:
```js
const isBalanceType = (type: string) => {
  return type === 'balance' || type === 'admin_balance' || type === 'invoice_fee'
}
```

- [ ] **Step 2: `getHistoryItemTitle` 新增 invoice_fee 分支**

Change `getHistoryItemTitle` (lines 393-406).

Old:
```js
const getHistoryItemTitle = (item: RedeemHistoryItem) => {
  if (item.type === 'balance') {
    return t('redeem.balanceAddedRedeem')
  } else if (item.type === 'admin_balance') {
    return item.value >= 0 ? t('redeem.balanceAddedAdmin') : t('redeem.balanceDeductedAdmin')
  } else if (item.type === 'concurrency') {
    return t('redeem.concurrencyAddedRedeem')
  } else if (item.type === 'admin_concurrency') {
    return item.value >= 0 ? t('redeem.concurrencyAddedAdmin') : t('redeem.concurrencyReducedAdmin')
  } else if (item.type === 'subscription') {
    return t('redeem.subscriptionAssigned')
  }
  return t('common.unknown')
}
```

New:
```js
const getHistoryItemTitle = (item: RedeemHistoryItem) => {
  if (item.type === 'balance') {
    return t('redeem.balanceAddedRedeem')
  } else if (item.type === 'admin_balance') {
    return item.value >= 0 ? t('redeem.balanceAddedAdmin') : t('redeem.balanceDeductedAdmin')
  } else if (item.type === 'invoice_fee') {
    return item.value >= 0 ? t('redeem.invoiceFeeRefunded') : t('redeem.invoiceFeeCharged')
  } else if (item.type === 'concurrency') {
    return t('redeem.concurrencyAddedRedeem')
  } else if (item.type === 'admin_concurrency') {
    return item.value >= 0 ? t('redeem.concurrencyAddedAdmin') : t('redeem.concurrencyReducedAdmin')
  } else if (item.type === 'subscription') {
    return t('redeem.subscriptionAssigned')
  }
  return t('common.unknown')
}
```

- [ ] **Step 3: i18n（zh）新增标题键**

In `frontend/src/i18n/locales/zh.ts`, change line 1164.

Old:
```js
    balanceDeductedAdmin: '余额扣除（管理员）',
```

New:
```js
    balanceDeductedAdmin: '余额扣除（管理员）',
    invoiceFeeCharged: '开票服务费扣除',
    invoiceFeeRefunded: '开票服务费退回',
```

- [ ] **Step 4: i18n（en）新增标题键**

In `frontend/src/i18n/locales/en.ts`, change line 1160.

Old:
```js
    balanceDeductedAdmin: 'Balance Deducted (Admin)',
```

New:
```js
    balanceDeductedAdmin: 'Balance Deducted (Admin)',
    invoiceFeeCharged: 'Invoice Fee Charged',
    invoiceFeeRefunded: 'Invoice Fee Refunded',
```

- [ ] **Step 5: 类型检查 + i18n 键校验**

Run:
```bash
cd frontend && npx vue-tsc --noEmit && \
grep -q "invoiceFeeCharged: '开票服务费扣除'" src/i18n/locales/zh.ts && \
grep -q "invoiceFeeCharged: 'Invoice Fee Charged'" src/i18n/locales/en.ts && echo OK
```
Expected: 类型检查无报错；末尾打印 `OK`。

- [ ] **Step 6: 提交**

```bash
cd /Users/zhengshan/projects/sub2api
git add frontend/src/views/user/RedeemView.vue frontend/src/i18n/locales/zh.ts frontend/src/i18n/locales/en.ts
git commit -m "feat(invoice): show invoice_fee in user redeem history"
```

---

### Task 8: 全量验证

**Files:** 无（仅运行验证）

- [ ] **Step 1: 后端构建 + vet + 单测**

Run:
```bash
cd backend && go build ./... && go vet ./internal/service/ ./internal/repository/ && \
go test ./internal/service/ -run 'Invoice' -count=1
```
Expected: 构建/vet 无输出；测试 `ok  github.com/Wei-Shaw/sub2api/internal/service`。

- [ ] **Step 2: 后端 gofmt 干净**

Run: `cd backend && gofmt -l internal/service/ internal/repository/ | grep -E 'invoice_ledger|invoice_service|invoice_admin_service|domain_constants|redeem_code_repo' || echo "gofmt clean"`
Expected: `gofmt clean`。

- [ ] **Step 3: 前端类型检查**

Run: `cd frontend && npx vue-tsc --noEmit && echo TYPECHECK_OK`
Expected: 末尾打印 `TYPECHECK_OK`，无类型报错。

- [ ] **Step 4: i18n 四个键在两语种均存在**

Run:
```bash
cd frontend && for k in "invoice_fee" "invoiceFeeCharged" "invoiceFeeRefunded"; do
  z=$(grep -c "$k" src/i18n/locales/zh.ts); e=$(grep -c "$k" src/i18n/locales/en.ts)
  echo "$k: zh=$z en=$e"
done
```
Expected: `invoice_fee: zh>=1 en>=1`、`invoiceFeeCharged: zh=1 en=1`、`invoiceFeeRefunded: zh=1 en=1`。

- [ ] **Step 5: 人工冒烟（可选，需运行环境）**

- 用足额余额用户提交一笔专票开票 → 管理员兑换码页出现 `发票服务费 / Invoice Fee`、金额为负（如 `$-6.00`）、`used_by` 为该用户；用户 `/redeem` 最近活动出现"开票服务费扣除"，金额红色 `-$6.00`。
- 管理员驳回该申请 → 两处新增"开票服务费退回 / Invoice Fee" 正数行；用户余额复原。
- 管理员"累计充值"不因退费正数行升高。

---

## 验收对照（spec → task）

| spec 要求 | 实现任务 |
|---|---|
| 新类型常量 `invoice_fee` | Task 1 Step 3 |
| 账本 helper（值正负、notes、status=used、used_by、合成 code）同事务原子写入 | Task 1（helper + 测试） |
| 提交扣费写负数账本行 | Task 2 |
| 驳回退费写正数账本行 | Task 3 |
| 取消退费写正数账本行 | Task 4 |
| `total_recharged` 排除 invoice_fee（白名单天然排除，仅注释防误改） | Task 5 |
| 管理员兑换码页显示 + 可筛选 + 金额格式 + i18n | Task 6 |
| 用户 `/redeem` 显示（扣除/退回标题 + 货币格式）+ i18n | Task 7 |
| 幂等（扣费随提交一次；退费有 `fee_charged_at/fee_refunded_at` 守卫） | 复用既有守卫，无需新增（Task 2/3/4 在守卫分支内写账本） |
| 无新迁移 | 全程不建迁移 |
| 历史不补写 | 不实现补写逻辑 |
