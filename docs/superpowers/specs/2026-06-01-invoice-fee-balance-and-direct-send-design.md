# 发票模块改造：专票费从余额收取 + 对公直发邮件 + 多附件

日期：2026-06-01
状态：待评审
前置：本设计**修改** [`2026-05-29-invoice-vat-special-fee-design.md`](./2026-05-29-invoice-vat-special-fee-design.md)（migration 145）确立的金额语义。

## 背景与问题

当前专票（增值税专用发票）逻辑：对一笔 1000 的充值开专票时，系统按 `invoice_amount = base − fee` 只开 **940**，把 6%（60）作为“开票费”从发票金额里扣减，**不触碰用户余额**。

两个缺陷：

1. **付款金额与发票金额对不上**：客户打款 1000、却收到 940 的专票，客户财务对账与进项抵扣会有问题。行业惯例是「开专票另加 6 个点」，6% 由客户承担。当前实现没有真正向客户收这 6%。
2. **对公打款无法开票**：有客户银行转账到对公账户，这部分钱系统里没有任何记录（无 `payment_order`），现有按订单开票的流程根本选不到，无法把发票发给客户。

## 目标

1. 专票按**全额**开票（开 1000），额外向客户收取 6% 服务费，**从用户余额扣除**；余额不足则拦截并提示充值。扣费前明确提醒用户。
2. 提供管理员**独立的「发送发票邮件」**工具，用于对公打款等无系统记录的场景，支持**多个附件**，并保留发送记录。
3. 普通订单开票完成时也支持**上传多个发票文件**。

## 非目标（本期不做）

- 不引入通用余额流水/双账记账表（项目当前没有，沿用 `users.balance` 单字段 + 发票记录作凭证）。
- 作废发票（订单退款导致 `voided`）时是否连带退 6%，**本期不处理**，后续单独议。
- 历史已创建的发票申请行不迁移、不补扣，保持原值。

---

## 决策记录

| 决策点 | 选择 |
|---|---|
| 开票模型 | 开全额 + 余额扣 6% |
| 扣费时机 | **提交申请时冻结**；驳回/取消时退回 |
| 费用收取方式 | 直接扣 `users.balance`（方案 A），不做独立订单 |
| 二次确认 | **专票提交时弹框确认才扣**；普票不弹 |
| 对公发邮件 | 独立工具，**发送并留记录** |
| 普通开票完成 | **也支持多附件** |

---

## Part 1：专票全额开票 + 提交时冻结 6%

### 1.1 金额语义变更（migration 146）

把 145 的「`invoice_amount = base − fee`」改为：

- `invoice_amount = base`（全额开票）
- `fee_amount = round2(base × feeRate)`（额外向余额收取的服务费）
- 两者不再满足 `fee + invoice = base`；新关系是 `invoice = base`，`fee` 为附加项。

`invoice_amount.go::computeInvoiceAmounts` 改写：

```go
func computeInvoiceAmounts(base, feeRate float64) (feeAmount, invoiceAmount float64) {
    invoiceAmount = round2(base)
    if feeRate <= 0 || base <= 0 {
        return 0, invoiceAmount
    }
    feeAmount = round2(base * feeRate)
    return feeAmount, invoiceAmount
}
```

`resolveInvoiceFeeConfig` 不变（仅专票适用费率，普票 0）。

**新增列**（`invoice_requests`，migration 146）：

- `fee_charged_at TIMESTAMPTZ NULL` —— 提交时扣费成功的时间。
- `fee_refunded_at TIMESTAMPTZ NULL` —— 驳回/取消退费时间（退费幂等 + 审计）。

历史行：`fee_charged_at/fee_refunded_at` 均为 NULL，金额列保持原值，不补扣不退。新逻辑只对**新提交**生效。

### 1.2 提交时扣费（`CreateInvoiceRequest`，同一事务）

在现有计算 `feeAmount, invoiceAmount` 之后、`INSERT invoice_requests` 前后同事务内：

1. 若 `feeAmount > 0`：
   - 原子扣减：`UPDATE users SET balance = balance - $fee WHERE id = $user AND balance >= $fee`
   - `RowsAffected == 0` → 视为余额不足：回滚事务，返回 `INVOICE_BALANCE_INSUFFICIENT`，错误体携带 `required`（=fee）、`balance`（当前余额）、`shortfall`（=fee−balance）。
   - 成功 → 写入 `fee_charged_at = NOW()`（可在 INSERT 时带上，或同事务 UPDATE）。
2. `feeAmount == 0`（普票或 rate=0）：不碰余额，行为与现状一致。
3. 事务提交**之后**：失效该用户余额缓存（见 1.4）。

注：`users` 与 `invoice_requests` 同一 Postgres 库，发票走 `database/sql` 原始事务，对 `users` 表做原始 SQL 扣减是原子的。不使用 `userRepo.DeductBalance`（它允许透支），改用带 `balance >= fee` 守卫的原始 UPDATE 以实现“余额不足即拦截”。

### 1.3 退费（驳回 / 取消）

退费幂等条件统一为：`fee_charged_at IS NOT NULL AND fee_refunded_at IS NULL`。

- **驳回 `RejectInvoiceRequest`**（`pending→rejected`，单条 `WHERE status=pending` 守卫天然只触发一次）：
  改为事务执行——先读出该行 `fee_amount/fee_charged_at/fee_refunded_at`，满足条件则 `UPDATE users SET balance = balance + $fee`，并在状态更新中写 `fee_refunded_at = NOW()`。提交后失效余额缓存。
- **取消 `CancelInvoiceRequest`**（当前为**硬删除**）：在 `DELETE FROM invoice_requests` **之前**、同事务内，若满足退费条件则 `balance += fee`（行将被删，无需写 `fee_refunded_at`）。提交后失效余额缓存。

### 1.4 余额缓存失效

余额有 billing/auth 缓存。原始 SQL 扣/退后需失效，复用 `UserService` 既有失效逻辑（`UpdateBalance` 内部已失效 auth + billing 缓存）。
实现期接线：让 `PaymentService` 能在事务提交后调用用户余额缓存失效（注入 `UserService` 或其缓存失效方法）。**这是实现期需要落实的接线点。**

### 1.5 前端 `InvoiceView.vue`

- 选择**专票**且已勾选订单时，展示服务费预览：
  「本次开具增值税专用发票需支付开票服务费 **¥{fee}**，将从账户余额扣除（当前余额 ¥{balance}，扣后 ¥{balance−fee}）」。fee 随勾选订单实时计算。
- **余额不足**：提交按钮置灰 + 文案「余额不足，开票服务费需 ¥{fee}，当前余额 ¥{balance}，还差 ¥{shortfall}」+「去充值」入口。
- **二次确认弹窗（仅专票）**：点提交后弹框——
  > 开具增值税专用发票需支付 6% 开票服务费 **¥{fee}**，将从账户余额扣除（余额 ¥{balance} → ¥{balance−fee}）。确认提交？ [取消] [确认并扣费]
  
  普票不弹，直接提交。
- 后端返回 `INVOICE_BALANCE_INSUFFICIENT` 时兜底弹同样提示。
- 提交成功 toast：「开票申请已提交，已扣除开票服务费 ¥{fee}」。
- 开票记录列表中该条显示「已扣服务费 ¥{fee}」；驳回/取消后显示「已退回 ¥{fee}」。

### 1.6 费率/类目下发给前端

前端预览需要费率。新增（或复用）一个轻量只读接口返回 `vat_special_fee_rate`、`service_category`，供前端计算预览。若已有公开 settings 接口可暴露则复用，否则新增 `GET /api/v1/invoice/config`。

---

## Part 2：管理员「发送发票邮件」（对公直发，带记录）

与订单/余额完全解耦的独立功能。

### 2.1 接口

`POST /api/v1/admin/invoice/send-email`（multipart）：

- `recipient_email`（必填，邮箱格式校验）
- `subject`（选填，留空走模板 `[{siteName}] 您的发票 / Your Invoice`）
- `note`（选填，正文备注）
- `files[]`（1..N 个附件）

校验复用现有规则：每个 ≤ `maxInvoiceFileBytes`(10MB)、MIME 限 `allowedInvoiceMimeTypes`（PDF/PNG/JPEG/ZIP/Excel）；建议附件数上限 5。
复用 `invoiceEmailSender.SendEmailWithAttachment`，把多个 `EmailAttachment` 一封邮件发出。

### 2.2 记录表（migration 147）`invoice_email_sends`

| 列 | 说明 |
|---|---|
| `id` | PK |
| `recipient_email` | 收件邮箱 |
| `subject` | 主题（最终发送的） |
| `note` | 备注 |
| `attachment_count` | 附件数 |
| `attachment_names` | JSONB，附件文件名列表 |
| `status` | `sent` / `failed` |
| `error_message` | 失败原因，NULL |
| `sent_by` | 管理员 user id |
| `sent_at` | 时间 |

不持久化附件文件本身，只存元数据（与现有完成开票“不落盘”一致）。发送成功后写一条 `sent`，失败写 `failed` + `error_message`。

### 2.3 管理员 UI（`AdminInvoicesView.vue`）

- 新增「直接发送发票」表单：收件邮箱、主题、备注、多文件选择、发送。
- 新增发送历史列表（读 `invoice_email_sends`，分页）。

---

## Part 3：普通订单开票完成支持多附件

### 3.1 后端 `CompleteInvoiceRequest`

- `CompleteInvoiceRequestInput.File *multipart.FileHeader` → `Files []*multipart.FileHeader`（1..N，上限 5，各 ≤10MB）。
- 逐个校验（大小、MIME），逐个读入内存，组装为多个 `EmailAttachment` 一封邮件发出。
- `invoice_no` 仍为单个主票号（语义不变）。
- 其余流程（仅 `pending` 可完成、邮件失败保持 pending、成功置 `completed`）不变。

### 3.2 handler / DTO

- 接收 multipart 文件数组（`files[]`）。
- admin 完成弹窗（`AdminInvoicesView.vue`）支持多选文件。

---

## 受影响文件清单

**后端**
- `migrations/146_invoice_fee_balance.sql`（新增 `fee_charged_at`/`fee_refunded_at`）
- `migrations/147_invoice_email_sends.sql`（新表）
- `internal/service/invoice_amount.go`（`computeInvoiceAmounts` 语义）
- `internal/service/invoice_service.go`（`CreateInvoiceRequest` 扣费 + 余额不足拦截）
- `internal/service/invoice_admin_service.go`（`Reject`/`Cancel` 退费；`Complete` 多附件；新增 `SendInvoiceEmail`）
- `internal/handler/invoice_handler.go` / `internal/handler/admin/invoice_handler.go`（多文件、新接口、config 接口）
- `internal/server/routes/payment.go`（路由注册）
- PaymentService 余额缓存失效接线（注入 UserService/缓存失效）

**前端**
- `frontend/src/views/user/InvoiceView.vue`（费用预览、二次确认、余额不足拦截、成功/退费提示）
- `frontend/src/views/admin/AdminInvoicesView.vue`（直发表单 + 历史、完成开票多文件）
- `frontend/src/api/invoice.ts`、`frontend/src/types/invoice.ts`（新接口、多文件、config、新增字段）

## 错误码

- `INVOICE_BALANCE_INSUFFICIENT` —— 余额不足以支付开票服务费（携带 required/balance/shortfall）。
- 复用现有 `INVOICE_FILE_*` 系列做多附件逐个校验。

## 测试要点

- 专票提交：余额足→扣费成功、`fee_charged_at` 写入、`invoice_amount==base`、`fee_amount==base×rate`。
- 专票提交：余额不足→拦截、不创建申请、余额不变。
- 普票提交：不扣费、行为不变。
- 驳回/取消：已扣费的退回、余额复原、幂等（重复不double退）；未扣费的不退。
- 扣/退后余额缓存一致。
- 对公发邮件：多附件发送成功写 `sent`、失败写 `failed`；附件校验。
- 完成开票多附件：多文件一封邮件、`invoice_no` 不变、各文件校验。

## 上线注意

- migration 146 上线后，**新开专票的发票金额变为全额**（金额变大），属预期正确行为；需同步知会运营/财务。
- 历史 pending 专票（若有）按旧语义存的 `invoice_amount=base−fee` 且未扣费——这些若继续完成会开 940 且未收 6%。建议上线前清掉历史 pending 专票或单独处理，规模应很小（实现期确认数量）。
