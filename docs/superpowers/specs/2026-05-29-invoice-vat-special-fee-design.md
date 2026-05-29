# 专票 6% 开票费 + 技术服务费类目 设计文档

日期:2026-05-29
分支:`feat/invoice-vat-special-fee`

## 1. 背景与目标

当前发票系统中,开票金额(`total_amount`)= 用户所选已完成支付订单的 `pay_amount` 之和,无任何税费或类目概念。

业务需求:

1. 用户申请**增值税专用发票(专票 / `vat_special`)** 时,平台收取 **6% 开票费**,该费用**从开票金额中扣除**——即实际开票金额 = 申请金额 × (1 − 6%)。发票按净额开具。
2. 必须在用户页面给出**显著提示**,告知专票将扣除 6% 开票费。
3. 所有发票的**开票类目固定为「技术服务费」**。
4. **普通发票(普票 / `general`)** 不收开票费,按申请金额全额开具;类目同样为「技术服务费」。

### 已确认的计算口径(以 ¥1000 为例)

```
申请金额(订单合计)      ¥1000.00
专票开票费 (6%)         -¥60.00
─────────────────────────────
实际开票金额            ¥940.00
→ 增值税专用发票金额: ¥940.00,开票类目: 技术服务费
```

普票:实际开票金额 = 申请金额(¥1000.00),无费用行。

## 2. 范围

- 仅 `vat_special` 专票扣 6%;`general` 费率为 0。
- 费率与类目通过管理员设置项配置,不硬编码;默认 6% / 「技术服务费」。
- 费率与类目在**创建申请时快照**进申请行,后续修改设置不影响历史申请(与现有 `profile_snapshot` 不可变思路一致)。

非目标(本次不做):

- 不引入额外支付/补款流程(6% 是从开票金额中扣除,不向用户额外收款)。
- 不改变退款耦合(`has_refunded_orders` / 作废)逻辑。
- 类目不做用户可选,固定为设置项的单一值。

## 3. 方案选型

| 方案 | 描述 | 取舍 | 结论 |
|---|---|---|---|
| **A** | 在 `invoice_requests` 增加显式拆分列(base/fee_rate/fee_amount/invoice_amount/service_category),创建时快照费率 | 账目清晰可审计;历史不受费率调整影响;前后端/邮件读同一净额 | **采用** |
| B | 不存,处处实时 `total_amount × 0.94` | 费率一改历史发票金额全变;前后端易不一致 | 否决 |
| C | 复用 `total_amount` 存净额 | 改变既有字段语义,申请金额丢失 | 否决 |

## 4. 数据模型(后端)

### 4.1 迁移 `137_invoice_vat_special_fee.sql`

在 `invoice_requests` 表新增 5 列:

| 列 | 类型 | 含义 | 历史行回填 |
|---|---|---|---|
| `base_amount` | DECIMAL(20,2) NOT NULL DEFAULT 0 | 申请金额 = 所选订单合计 | = `total_amount` |
| `fee_rate` | DECIMAL(5,4) NOT NULL DEFAULT 0 | 开票费率(专票 0.06,普票 0) | 0 |
| `fee_amount` | DECIMAL(20,2) NOT NULL DEFAULT 0 | 开票费 = round2(base × rate) | 0 |
| `invoice_amount` | DECIMAL(20,2) NOT NULL DEFAULT 0 | **实际开票金额** = base − fee | = `total_amount` |
| `service_category` | VARCHAR(64) NOT NULL DEFAULT '技术服务费' | 开票类目 | '技术服务费' |

回填语句(对存量数据):

```sql
UPDATE invoice_requests
SET base_amount = total_amount,
    invoice_amount = total_amount
WHERE base_amount = 0;
```

`total_amount` **保持不变**(仍 = 申请金额合计),仅为兼容旧字段/旧查询;新逻辑一律读 `invoice_amount`。

### 4.2 结构体变更

`backend/internal/service/invoice_service.go` — `InvoiceRequest`(约 74-101 行)新增字段:

```go
BaseAmount      float64 `json:"base_amount"`
FeeRate         float64 `json:"fee_rate"`
FeeAmount       float64 `json:"fee_amount"`
InvoiceAmount   float64 `json:"invoice_amount"`
ServiceCategory string  `json:"service_category"`
```

DTO(对外返回)同步补齐这些字段。

### 4.3 设置项(走现有 SettingService)

| key | 默认值 | 含义 |
|---|---|---|
| `invoice_vat_special_fee_rate` | `0.06` | 专票开票费率 |
| `invoice_service_category` | `技术服务费` | 开票类目 |

读取时做兜底:解析失败或未设置 → 用默认值(0.06 / 技术服务费)。

## 5. 计算逻辑(后端)

位置:`CreateInvoiceRequest`(`invoice_service.go` 约 424-512 行),在现有事务内、求得 `base_amount` 之后计算:

```
base_amount    = Σ order.pay_amount                     // 现有逻辑
fee_rate       = profile_snapshot.invoice_type == "vat_special" ? setting.feeRate : 0
fee_amount     = round2(base_amount × fee_rate)         // 四舍五入到分(half-up)
invoice_amount = base_amount − fee_amount               // 相减反推,保证三者对账平
service_category = setting.serviceCategory
total_amount   = base_amount                            // 兼容旧字段
```

要点:

- `invoice_amount` 用 **`base − fee`** 反推,而非 `base × 0.94` 直算,避免两次独立舍入对不上(如 base=¥333.33 → fee=20.00 → net=313.33 始终自洽)。
- 舍入统一为四舍五入到 2 位小数(分)。建议用整数分运算或 `math.Round(x*100)/100` 封装为单一 helper `round2`,供计算复用。
- 费率与类目此刻**快照**写入申请行;之后管理员改设置只影响新申请。

## 6. 前端 —— 用户页面

文件:`frontend/src/views/user/InvoiceView.vue`(「可开票订单」Tab,约 573-625 行表单/合计区)。

### 6.1 显著提示 banner(仅所选抬头为专票时)

醒目警示色 alert(非小字脚注),文案随费率动态渲染:

```
⚠ 专票须知
增值税专用发票将收取 {rate}% 开票费,从开票金额中扣除。
实际开票金额 = 申请金额 × {100−rate}%,开票类目为技术服务费。
```

### 6.2 金额拆分区

```
申请金额(订单合计)        ¥1000.00
专票开票费 (6%)          -¥60.00      ← 仅专票显示
─────────────────────────────────
实际开票金额              ¥940.00
开票类目                  技术服务费
```

- 选中**普票**抬头:不显示 banner 与费用行,仅显示「开票金额 ¥1000.00」+ 类目。
- 前端拆分仅用于**预览展示**;真实金额以后端创建时计算为准(前端不作为可信来源)。
- 开票记录列表/详情:主显 `invoice_amount`,专票行附「含 {rate}% 开票费」拆分。

## 7. 前端 —— 管理员页面

文件:`frontend/src/views/admin/AdminInvoicesView.vue`。

- 列表/详情新增列:申请金额 / 开票费 / **实际开票金额** / 开票类目。
- 「完成开票」对话框(约 173-234 行)顶部明确提示:
  「请按**实际开票金额 ¥940.00**、类目**技术服务费**开具增值税专用发票」。
- 发送给用户的发票邮件正文引用 `invoice_amount` 与 `service_category`(邮件构建逻辑见 invoice 邮件附件相关代码)。

## 8. i18n

`frontend/src/i18n/locales/zh.ts` 与 `en.ts` 的 `invoice` 节对称新增:

| key | 中文 | English |
|---|---|---|
| `fields.baseAmount` | 申请金额 | Requested Amount |
| `fields.invoiceFee` | 专票开票费 | VAT-Special Fee |
| `fields.invoiceAmount` | 实际开票金额 | Invoice Amount |
| `fields.serviceCategory` | 开票类目 | Category |
| `messages.vatSpecialFeeNotice` | 见 §6.1 文案 | 同义英文 |

数值(费率/百分比)以参数插值,不写死「6%」。

## 9. 测试

后端单元测试(`//go:build unit`,`invoice_service` 包):

- 专票:base=1000 → fee=60、invoice_amount=940、service_category=技术服务费。
- 普票:fee=0、invoice_amount=base。
- 舍入自洽:base=333.33 → fee+net=base。
- 快照不变性:创建后修改设置费率,已建申请的 fee_rate/invoice_amount 不变。
- 设置缺省兜底:未配置设置项时用默认 0.06 / 技术服务费。

前端:金额拆分组件断言(专票显示费用行、普票不显示)。

## 10. 涉及文件清单

后端:

- `backend/migrations/137_invoice_vat_special_fee.sql`(新增)
- `backend/internal/service/invoice_service.go`(结构体 + 计算 + 设置读取)
- `backend/internal/service/invoice_service_test.go`(`//go:build unit`,新增/补充)
- DTO 层 invoice 返回结构(补字段)
- 设置项默认值注册处(SettingService 默认值表)

前端:

- `frontend/src/views/user/InvoiceView.vue`
- `frontend/src/views/admin/AdminInvoicesView.vue`
- `frontend/src/api/...`(invoice 类型定义补字段)
- `frontend/src/i18n/locales/zh.ts`、`en.ts`

## 11. 兼容性与风险

- 存量发票申请:迁移回填 `base_amount`/`invoice_amount = total_amount`,`fee_*`=0,类目默认值;历史展示不变。
- `total_amount` 语义保持不变,旧查询/旧前端字段不破坏。
- 费率快照保证财务可追溯;调整税点不污染历史。
- 退款耦合 / 作废逻辑不受影响。
