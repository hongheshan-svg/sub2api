# 发票服务费扣费/退费写入余额流水账本（可在兑换码页与用户余额历史查询）

日期：2026-06-06
状态：核心决策已确认（退费记正数账本行；`invoice_fee` 排除出累计充值统计），待进入实现计划
前置：本设计**扩展** [`2026-06-01-invoice-fee-balance-and-direct-send-design.md`](./2026-06-01-invoice-fee-balance-and-direct-send-design.md)（专票全额开票 + 余额扣 6%）。本设计**不改变**扣费/退费的金额语义与时机，只在原有扣费/退费动作旁**增加一条余额流水账本记录**。

## 背景与问题

当前专票开票服务费（6%）的处理（见前置 spec）：

- 提交开票时从 `users.balance` 直接原子扣减（`invoice_service.go::CreateInvoiceRequest`，`UPDATE users SET balance = balance - fee WHERE id=? AND balance>=?`，同事务内 `INSERT invoice_requests`，并写 `fee_charged_at`）。
- 驳回 / 取消时退回（`invoice_admin_service.go::RejectInvoiceRequest` / `CancelInvoiceRequest`，`UPDATE users SET balance = balance + fee`，写 `fee_refunded_at`）。

**问题**：这两次余额变动**只记录在 `invoice_requests` 行上**（`fee_amount` + `fee_charged_at` / `fee_refunded_at`），**没有进入任何"余额流水/账本"**。因此：

- 管理员在**兑换码页**（`RedeemView.vue` admin，列出 `redeem_codes` 账本，含充值、`admin_balance` 手动调整等）看不到开票扣费。
- 用户在 **`/redeem` 余额变动历史**（"最近动态"，读同一 `redeem_codes` 账本）也看不到这笔扣费。
- 余额对账时，开票扣/退是"凭空"发生的，无统一流水可查。

系统已有一套以 `redeem_codes` 表充当**通用余额流水账本**的成熟先例：管理员在用户管理页手动调余额时，`admin_service.go::UpdateUserBalance`（约 952–1015 行）会写一条 `type=admin_balance`、`value=balanceDiff`（**可为负**）、`status=used`、`used_by=用户`、`code=GenerateRedeemCode()` 合成 的 `redeem_codes` 行。该记录天然出现在管理员兑换码页与用户 `/redeem` 历史里。

## 目标

1. 用户**提交开票被扣 6% 服务费**时，在**同一事务**内向 `redeem_codes` 写一条 `type=invoice_fee`、`value=-feeAmount` 的账本记录。
2. **驳回 / 取消退费**时，同事务写一条 `type=invoice_fee`、`value=+feeAmount` 的账本记录。
3. 这两类记录**自动出现**在：
   - 管理员**兑换码页**（`GET /admin/redeem-codes` 列表，后端不限类型）；并在筛选下拉中可按 `invoice_fee` 过滤。
   - 用户 **`/redeem` 余额变动历史**（`GET /redeem/history`，按用户取全部类型）。
4. 两侧前端正确渲染 `invoice_fee` 的**类型标签**与**金额方向**（扣除/退回），含中英文 i18n。

## 非目标（本期不做）

- **不新建**独立流水表 / 独立接口 / 独立页面（复用 `redeem_codes` + 现有兑换码页 + 现有 `/redeem` 页）。
- **不补写历史**：本功能上线前已有 `fee_charged_at` / `fee_refunded_at` 的发票申请，不回填账本（与前置 spec "历史行不迁移" 一致）。
- **不改变**扣费 / 退费的金额、时机、幂等条件（沿用前置 spec）。
- **不新增数据库迁移**：`redeem_codes` 现有列已满足（见 Part 1）。

---

## 决策记录

| 决策点 | 选择 |
|---|---|
| 记录载体 | 复用 `redeem_codes` 账本，新增 `type=invoice_fee` |
| 金额方向 | 扣费 `value = -feeAmount`；退费 `value = +feeAmount` |
| 是否记退费 | **记**（双向记账，便于对账，与 `admin_balance` 一致） |
| 写入原子性 | 账本 `INSERT` 与余额 `UPDATE` 在**同一原始事务**内，强一致 |
| 写入方式 | 原始 SQL `INSERT INTO redeem_codes`（事务为 `database/sql`，不混用 ent） |
| 管理员查询 | 兑换码页自动显示 + 筛选项增加 `invoice_fee` |
| 用户查询 | `/redeem` 余额变动历史自动显示（补类型渲染分支） |
| `total_recharged` 统计 | **天然排除** `invoice_fee`（`SumPositiveBalanceByUser` 仅白名单 `balance`/`admin_balance`，无需改码；**切勿**把 `invoice_fee` 加入白名单） |
| 历史数据 | 不补写 |
| 新迁移 | 无 |

---

## Part 1：账本模型与类型常量

### 1.1 复用 `redeem_codes`（无新迁移）

`redeem_codes` 现有列（`migrations/001_init.sql` + `004_add_redeem_code_notes.sql` + `137_redeem_code_expires_at.sql` 等）：

```
id BIGSERIAL PK
code        VARCHAR(32) NOT NULL UNIQUE
type        VARCHAR(20) NOT NULL DEFAULT 'balance'
value       DECIMAL(20,8) NOT NULL
status      VARCHAR(20) NOT NULL DEFAULT 'unused'
used_by     BIGINT NULL  (FK users ON DELETE SET NULL)
used_at     TIMESTAMPTZ NULL
notes       TEXT NULL
created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
expires_at  TIMESTAMPTZ NULL
group_id    BIGINT NULL
validity_days INT  (默认值，见实现期校验)
```

`invoice_fee` 行所需列均已存在；可空 / 带默认列（`expires_at` / `group_id` / `validity_days`）留空或走默认即可。**实现期需确认** `validity_days` 列的 NOT NULL/DEFAULT 约束，必要时在 `INSERT` 中显式给 `0`。

### 1.2 类型常量

在 `internal/service/domain_constants.go` 新增（与 `AdjustmentTypeAdminBalance` 并列）：

```go
// 开票服务费余额流水（提交扣费为负、驳回/取消退费为正）
const RedeemTypeInvoiceFee = "invoice_fee"
```

### 1.3 每条账本行语义

| 字段 | 扣费（提交开票） | 退费（驳回 / 取消） |
|---|---|---|
| `code` | `GenerateRedeemCode()` 合成 | 同左 |
| `type` | `invoice_fee` | `invoice_fee` |
| `value` | `-feeAmount`（负，余额单位，与 `users.balance` 同单位） | `+feeAmount`（正） |
| `status` | `used` | `used` |
| `used_by` | 申请用户 id | 申请用户 id |
| `used_at` | `NOW()` | `NOW()` |
| `created_at` | `NOW()`（默认） | `NOW()`（默认） |
| `notes` | `发票服务费 · 申请 {serial_no}` | `发票服务费退回 · 申请 {serial_no}` |

> `value` 单位说明：`feeAmount` 即从 `users.balance` 直接扣减的数值，与 `admin_balance` 存 `balanceDiff` 的单位完全一致，**无需换算**（`redeem_codes.value` 注释中的 "USD" 为历史遗留，实际语义是"余额单位"）。

---

## Part 2：后端写入（原子，关键）

事务背景：发票扣费 / 退费走 `database/sql` 原始事务（`tx`）。账本 `INSERT` 必须用**同一 `tx`** 的原始 SQL 完成，**不可**改用基于 ent 的 `redeemCodeRepo.Create`（它走独立连接 / 事务，无法与原始 `tx` 原子）。

### 2.1 公共 helper

新增 `internal/service/invoice_fee.go`（或新文件 `invoice_ledger.go`）：

```go
// insertInvoiceFeeLedgerTx 在给定事务内写一条开票服务费余额流水。
// value 为负=扣费，为正=退费。note 写入 notes 便于追溯（调用方用 serial_no 拼好）。
func insertInvoiceFeeLedgerTx(ctx context.Context, tx *sql.Tx, userID int64, value float64, note string) error {
    code, err := GenerateRedeemCode()
    if err != nil {
        return err
    }
    _, err = tx.ExecContext(ctx, `
        INSERT INTO redeem_codes (code, type, value, status, used_by, used_at, notes, created_at)
        VALUES ($1, $2, $3, 'used', $4, NOW(), $5, NOW())
    `, code, RedeemTypeInvoiceFee, value, userID, note)
    return err
}
```

（若 `validity_days` 为 NOT NULL 无默认，则在列与 `VALUES` 中补 `validity_days=0`。）

### 2.2 扣费写账本（`CreateInvoiceRequest`）

在现有 `UPDATE users SET balance = balance - fee ...`（约 488–507 行）**成功之后、`COMMIT` 之前**，同一 `tx` 内调用：

```go
note := fmt.Sprintf("发票服务费 · 申请 %s", serialNo)
if err := insertInvoiceFeeLedgerTx(ctx, tx, userID, -feeAmount, note); err != nil {
    return nil, infraerrors.InternalServer("INVOICE_REQUEST_CREATE_FAILED", "failed to record invoice fee ledger").WithCause(err)
}
```

- 仅 `feeAmount > 0` 时写（与扣费分支同条件）。
- 失败 → 整笔事务回滚（不扣费、不建申请、不写账本），保证账本 ⟺ 余额强一致。
- `serialNo` 在 `INSERT invoice_requests` 时已生成；如生成顺序在 helper 之后，调整为先得到 `serial_no` 再写账本（实现期按现有变量顺序接线）。

### 2.3 退费写账本（`RejectInvoiceRequest` / `CancelInvoiceRequest`）

退费幂等守卫不变：`fee_charged_at IS NOT NULL AND fee_refunded_at IS NULL`。在现有 `UPDATE users SET balance = balance + fee ...`（驳回约 305–309 行 / 取消约 245–249 行）成功后、同一 `tx` 内：

```go
note := fmt.Sprintf("发票服务费退回 · 申请 %s", serialNo)
if err := insertInvoiceFeeLedgerTx(ctx, tx, userID, +feeAmount, note); err != nil {
    return nil, infraerrors.InternalServer("...", "failed to record invoice fee refund ledger").WithCause(err)
}
```

- 仅在满足退费条件、确实执行了 `balance += fee` 的分支内写。
- 驳回：`WHERE status='pending'` 守卫天然只触发一次 → 退费账本只一条。
- 取消（硬删除 `invoice_requests`）：账本行独立于 `invoice_requests`，删申请不影响账本；同事务先 `balance += fee` + 写账本，再 `DELETE`。

### 2.4 余额缓存失效

沿用前置 spec 的失效逻辑：扣 / 退提交后失效用户余额缓存。账本写入不改变该流程（账本不参与缓存）。

---

## Part 3：管理员侧（兑换码页）

### 3.1 后端
- `GET /admin/redeem-codes`（`handler/admin/redeem_handler.go`，repo `ListWithFilters`）**不限类型**，`invoice_fee` 行自动出现。**无需改后端列表逻辑。**
- 仅当显式传 `?type=invoice_fee` 时按类型过滤——已支持。

### 3.2 前端 `views/admin/RedeemView.vue`
- 筛选下拉 `filterTypeOptions` 增加 `invoice_fee` 项（便于"方便管理员查询"）。
- 类型 badge：`t('admin.redeem.types.' + value)` 已动态渲染；为 `invoice_fee` 补一个 badge 样式分支（默认样式亦可）。
- `value` 列已能显示负值，无需改。

### 3.3 i18n
`admin.redeem.types.invoice_fee`：
- zh：`发票服务费`
- en：`Invoice Fee`

---

## Part 4：用户侧（`/redeem` 余额变动历史）

### 4.1 后端
- `GET /redeem/history`（`handler/redeem_handler.go` → `GetUserHistory` → `ListByUser`）**不按类型过滤**，`invoice_fee` 行自动出现。**无需改后端。**

### 4.2 前端 `views/user/RedeemView.vue`
现有 `isBalanceType()`（约 382 行）与 `activityTitle()`（约 395–403 行）只认 `balance/admin_balance/concurrency/...`，未知类型会被**错当成并发数**渲染（约 419 行 `${value} requests`）。必须补 `invoice_fee` 分支：

- `isBalanceType(type)`：加入 `invoice_fee` → 按**金额**（货币）格式化，而非并发数。
- `activityTitle(item)`：加 `invoice_fee` 分支——
  - `item.value < 0` → `t('redeem.invoiceFeeCharged')`（开票服务费扣除）
  - `item.value >= 0` → `t('redeem.invoiceFeeRefunded')`（开票服务费退回）
- 金额展示沿用余额类的正负 / 货币格式。

### 4.3 i18n
用户侧 `redeem.*`：
- zh：`invoiceFeeCharged: '开票服务费扣除'`，`invoiceFeeRefunded: '开票服务费退回'`
- en：`invoiceFeeCharged: 'Invoice Fee Charged'`，`invoiceFeeRefunded: 'Invoice Fee Refunded'`

---

## Part 5：一致性与边界

- **幂等**：扣费随提交事务一次；退费有 `fee_charged_at/fee_refunded_at` 守卫，各一次，天然不重复写账本。
- **强一致**：账本 `INSERT` 与余额 `UPDATE` 同事务，失败回滚，不存在"扣了余额没记账"或"记了账没扣余额"。
- **`total_recharged` 统计**：管理员余额历史（`GetUserBalanceHistory` / `GET /admin/users/:id/balance-history`）的"累计充值"由 `redeemCodeRepo.SumPositiveBalanceByUser` 计算，该方法**已用白名单 `TypeIn("balance","admin_balance")`** 求和，因此退费的正数 `invoice_fee` 行**天然不计入**——**无需改码**。实现期唯一要求：**切勿**把 `invoice_fee` 加进该白名单（加注释防误改）。
- **`code` 唯一冲突**：`GenerateRedeemCode()` 随机，冲突概率极低；沿用现有单次生成方式，冲突即事务失败（扣费场景极罕见地拦截一次提交，可接受；实现期可选加一次重试）。
- **合成 code 不可被兑换**：写入即 `status='used'`，不会被用户当兑换码再次使用（与 `admin_balance` 一致）。
- **历史数据**：不补写。

---

## 受影响文件清单

**后端**
- `internal/service/domain_constants.go`：新增 `RedeemTypeInvoiceFee = "invoice_fee"`。
- `internal/service/invoice_fee.go`（或新增 `invoice_ledger.go`）：新增 `insertInvoiceFeeLedgerTx`。
- `internal/service/invoice_service.go`：`CreateInvoiceRequest` 扣费成功后同事务写账本（负值）。
- `internal/service/invoice_admin_service.go`：`RejectInvoiceRequest` / `CancelInvoiceRequest` 退费分支同事务写账本（正值）。
- `internal/repository/redeem_code_repo.go`：`SumPositiveBalanceByUser` **无需改逻辑**（白名单已排除 `invoice_fee`），仅加一行注释防止后人误把 `invoice_fee` 加入白名单。
- **无新迁移。**

**前端**
- `frontend/src/views/admin/RedeemView.vue`：筛选项 + badge 样式。
- `frontend/src/views/user/RedeemView.vue`：`isBalanceType` / `activityTitle` 增加 `invoice_fee` 分支。
- `frontend/src/i18n/locales/zh.ts`、`en.ts`：`admin.redeem.types.invoice_fee` + `redeem.invoiceFeeCharged/Refunded`。

## 错误处理

- 账本写入失败 → 扣费场景返回既有 `INVOICE_REQUEST_CREATE_FAILED`、退费场景返回既有 `INVOICE_REQUEST_REJECT_FAILED` / `INVOICE_REQUEST_CANCEL_FAILED`，并回滚事务。不新增对外错误码。

## 测试要点

**后端**
- 提交开票（`feeAmount>0`）：余额扣减成功后，`redeem_codes` 新增一行 `type=invoice_fee`、`value=-feeAmount`、`status=used`、`used_by=用户`、`notes` 含 `serial_no`；与 `invoice_requests` 同事务。
- 余额不足：事务回滚，**无** `invoice_fee` 账本行，余额不变，无申请。
- 账本写入失败（模拟）：整笔回滚，余额不变、无申请、无账本行。
- 驳回 / 取消已扣费申请：新增一行 `value=+feeAmount`；重复触发不产生第二条（幂等守卫）。
- 未扣费（历史 / `feeAmount==0`）申请驳回 / 取消：不写账本。
- `total_recharged`：含 `invoice_fee` 退费正数行的用户，其"累计充值"不被该行抬高。

**前端**
- 管理员兑换码列表：出现 `invoice_fee` 行，类型标签为"发票服务费 / Invoice Fee"，可用筛选项过滤，负值正确显示。
- 用户 `/redeem` 最近动态：`invoice_fee` 行显示"开票服务费扣除/退回"，按货币金额（非并发数）格式化，正负方向正确。
- zh / en 两套 i18n 键齐全。

## 上线注意

- 仅对**上线后新发生**的扣费 / 退费写账本；历史扣费不在账本中（与前置 spec 一致），如运营需要历史可查需另行评估补写。
- 上线后，用户 `/redeem` 与管理员兑换码页会开始出现 `invoice_fee` 流水，属预期；需同步知会运营该类型含义（开票服务费扣除 / 退回）。
