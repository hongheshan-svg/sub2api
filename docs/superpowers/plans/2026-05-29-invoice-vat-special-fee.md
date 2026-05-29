# 专票 6% 开票费 + 技术服务费类目 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用户申请增值税专用发票时,从开票金额中扣除 6% 开票费(实际开票金额 = 申请金额 × 94%),所有发票类目固定为「技术服务费」,费率与类目均由管理员设置项动态配置,并在用户页给出显著提示。

**Architecture:** 后端在 `CreateInvoiceRequest` 创建申请时一次性计算并快照 base/fee_rate/fee_amount/invoice_amount/service_category 五个字段(方案 A);费率与类目从注入了 SettingService 的 PaymentService 读取,nil 安全回退默认值;同时把费率与类目通过现有 PublicSettings 链路下发给前端用于预览。`total_amount` 语义保持不变以兼容旧数据。

**Tech Stack:** Go (database/sql + lib/pq), PostgreSQL 迁移, Vue 3 + TypeScript, vue-i18n。

参考 spec:`docs/superpowers/specs/2026-05-29-invoice-vat-special-fee-design.md`

---

## File Structure

后端:
- `backend/migrations/145_invoice_vat_special_fee.sql`(新建)— 新增 5 列 + 回填
- `backend/internal/service/invoice_amount.go`(新建)— 纯金额计算 helper + 默认常量
- `backend/internal/service/invoice_amount_test.go`(新建,`//go:build unit`)— 计算单元测试
- `backend/internal/service/invoice_service.go`(改)— 结构体字段、列常量、scan、INSERT、计算接线
- `backend/internal/pkg/constants/...` 或 `domain_constants.go`(改)— 2 个 setting key 常量
- `backend/internal/service/setting_service.go`(改)— 2 个 getter + PublicSettings 接线
- `backend/internal/service/settings_view.go`(改)— service.PublicSettings 加 2 字段
- `backend/internal/handler/dto/settings.go`(改)— dto.PublicSettings 加 2 字段
- `backend/internal/handler/setting_handler.go`(改)— service→dto 映射加 2 字段

前端:
- `frontend/src/types/invoice.ts`(改)— InvoiceRequest 加 5 字段
- `frontend/src/types/index.ts`(改)— PublicSettings 加 2 字段
- `frontend/src/stores/app.ts`(改)— fallback 默认值 + 暴露 2 个 computed
- `frontend/src/views/user/InvoiceView.vue`(改)— 提示 banner + 金额拆分 + 记录展示
- `frontend/src/views/admin/AdminInvoicesView.vue`(改)— 拆分展示 + 完成开票提示
- `frontend/src/i18n/locales/zh.ts`、`en.ts`(改)— 新增键

---

## Task 1: 数据库迁移

**Files:**
- Create: `backend/migrations/145_invoice_vat_special_fee.sql`

迁移号:仓库现有迁移最高到 `144_*`(`136/137` 有历史重号但已过),新号用 **145**。

- [ ] **Step 1: 写迁移文件**

```sql
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
```

- [ ] **Step 2: 提交**

```bash
git add backend/migrations/145_invoice_vat_special_fee.sql
git commit -m "feat(invoice): migration for 专票 fee + 技术服务费 columns"
```

---

## Task 2: 纯金额计算 helper(TDD)

**Files:**
- Create: `backend/internal/service/invoice_amount.go`
- Test: `backend/internal/service/invoice_amount_test.go`

业务规则:`fee = round2(base × rate)`;`invoice = round2(base) − fee`(相减反推,保证三者对账平)。普票 rate=0 → fee=0、invoice=base。

- [ ] **Step 1: 写失败测试**

`backend/internal/service/invoice_amount_test.go`:

```go
//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRound2(t *testing.T) {
	require.Equal(t, 20.00, round2(19.9998))
	require.Equal(t, 60.00, round2(60.0))
	require.Equal(t, 313.33, round2(313.3299))
}

func TestComputeInvoiceAmounts(t *testing.T) {
	// 专票 6%
	fee, inv := computeInvoiceAmounts(1000.0, 0.06)
	require.Equal(t, 60.00, fee)
	require.Equal(t, 940.00, inv)

	// 普票(费率 0)
	fee, inv = computeInvoiceAmounts(1000.0, 0)
	require.Equal(t, 0.00, fee)
	require.Equal(t, 1000.00, inv)

	// 舍入自洽:fee+invoice 必须等于 base
	fee, inv = computeInvoiceAmounts(333.33, 0.06)
	require.Equal(t, 20.00, fee)
	require.Equal(t, 313.33, inv)
	require.Equal(t, 333.33, round2(fee+inv))

	// 零金额
	fee, inv = computeInvoiceAmounts(0, 0.06)
	require.Equal(t, 0.00, fee)
	require.Equal(t, 0.00, inv)
}
```

- [ ] **Step 2: 运行,确认失败**

Run: `cd backend && go test -tags unit ./internal/service/ -run 'TestRound2|TestComputeInvoiceAmounts' -v`
Expected: 编译失败 `undefined: round2` / `undefined: computeInvoiceAmounts`

- [ ] **Step 3: 写实现**

`backend/internal/service/invoice_amount.go`:

```go
package service

import "math"

// 发票计算默认值;管理员未配置或配置非法时回退到这些值。
const (
	// InvoiceVATSpecialFeeRateDefault 是增值税专用发票的默认开票费率(6%)。
	InvoiceVATSpecialFeeRateDefault = 0.06
	// InvoiceServiceCategoryDefault 是默认开票类目。
	InvoiceServiceCategoryDefault = "技术服务费"
)

// round2 四舍五入到 2 位小数(分)。
func round2(x float64) float64 {
	return math.Round(x*100) / 100
}

// computeInvoiceAmounts 由申请金额与费率计算开票费与实际开票金额。
// invoiceAmount 用 base−fee 反推,保证 fee+invoice 始终等于 base(对账平)。
func computeInvoiceAmounts(base, feeRate float64) (feeAmount, invoiceAmount float64) {
	if feeRate <= 0 || base <= 0 {
		return 0, round2(base)
	}
	feeAmount = round2(base * feeRate)
	invoiceAmount = round2(base) - feeAmount
	return feeAmount, invoiceAmount
}
```

- [ ] **Step 4: 运行,确认通过**

Run: `cd backend && go test -tags unit ./internal/service/ -run 'TestRound2|TestComputeInvoiceAmounts' -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/service/invoice_amount.go backend/internal/service/invoice_amount_test.go
git commit -m "feat(invoice): pure helpers for fee + invoice amount"
```

---

## Task 3: 结构体字段 + 列常量 + scan

**Files:**
- Modify: `backend/internal/service/invoice_service.go`(struct 约 74-101;`invoiceRequestColumns` 约 615;`scanInvoiceRequest` 约 617-655)

- [ ] **Step 1: 给 InvoiceRequest 结构体加字段**

在 `InvoiceRequest` 结构体的 `Orders []InvoiceRequestOrder` 行之后、`// Admin-processing fields` 之前插入:

```go
	// 金额拆分与类目(创建时快照)
	BaseAmount      float64 `json:"base_amount"`
	FeeRate         float64 `json:"fee_rate"`
	FeeAmount       float64 `json:"fee_amount"`
	InvoiceAmount   float64 `json:"invoice_amount"`
	ServiceCategory string  `json:"service_category"`
```

- [ ] **Step 2: 扩展 invoiceRequestColumns 常量**

把(约 615 行):

```go
const invoiceRequestColumns = `id, user_id, profile_id, serial_no, status, profile_snapshot, total_amount::float8, reject_reason, created_at, updated_at, completed_at, invoice_no, invoice_file_path, invoice_file_size, invoice_file_name, invoice_file_mime, processed_by, processed_at, has_refunded_orders, voided_at, voided_reason`
```

改为(末尾追加 5 列,顺序须与 scan 一致):

```go
const invoiceRequestColumns = `id, user_id, profile_id, serial_no, status, profile_snapshot, total_amount::float8, reject_reason, created_at, updated_at, completed_at, invoice_no, invoice_file_path, invoice_file_size, invoice_file_name, invoice_file_mime, processed_by, processed_at, has_refunded_orders, voided_at, voided_reason, base_amount::float8, fee_rate::float8, fee_amount::float8, invoice_amount::float8, service_category`
```

- [ ] **Step 3: 扩展 scanInvoiceRequest 的 Scan 目标**

在 `scanInvoiceRequest` 的 `scanner.Scan(...)` 调用里,`&req.VoidedReason,` 之后追加:

```go
		&req.BaseAmount,
		&req.FeeRate,
		&req.FeeAmount,
		&req.InvoiceAmount,
		&req.ServiceCategory,
```

- [ ] **Step 4: 编译确认**

Run: `cd backend && go build ./internal/service/`
Expected: 成功(此时 INSERT 仍未写新列,但 RETURNING 列数与 scan 数已一致 = 26 列)

> 注意:此刻 INSERT 还是旧的 6 列,RETURNING 用的是 invoiceRequestColumns(26 列),scan 也是 26 列 → 一致,可编译可运行;新列取默认值。Task 5 再写入真实值。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/service/invoice_service.go
git commit -m "feat(invoice): add fee/amount/category fields to InvoiceRequest scan"
```

---

## Task 4: Setting key 常量 + SettingService getter

**Files:**
- Modify: `backend/internal/service/domain_constants.go`(setting key 常量区,`SettingKeyAffiliateRebateRate` 附近,约 125-198)
- Modify: `backend/internal/service/setting_service.go`(getter,参考 `GetAffiliateRebateRatePercent` 约 2385、`GetSiteName` 约 2475)

- [ ] **Step 1: 加 setting key 常量**

在 `domain_constants.go` 的 setting key 常量块内追加:

```go
	SettingKeyInvoiceVATSpecialFeeRate = "invoice_vat_special_fee_rate" // 专票开票费率(0-1 小数,默认 0.06)
	SettingKeyInvoiceServiceCategory   = "invoice_service_category"     // 开票类目(默认 技术服务费)
```

- [ ] **Step 2: 加两个 getter**

在 `setting_service.go` 末尾(或 affiliate getters 附近)追加。常量 `InvoiceVATSpecialFeeRateDefault` / `InvoiceServiceCategoryDefault` 来自 Task 2(同包)。`strconv`、`strings`、`math` 该文件已 import。

```go
// GetInvoiceVATSpecialFeeRate 返回专票开票费率(小数,0<=rate<1);非法或未配置回退默认 0.06。
func (s *SettingService) GetInvoiceVATSpecialFeeRate(ctx context.Context) float64 {
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyInvoiceVATSpecialFeeRate)
	if err != nil {
		return InvoiceVATSpecialFeeRateDefault
	}
	rate, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 || rate >= 1 {
		return InvoiceVATSpecialFeeRateDefault
	}
	return rate
}

// GetInvoiceServiceCategory 返回开票类目;空或未配置回退默认「技术服务费」。
func (s *SettingService) GetInvoiceServiceCategory(ctx context.Context) string {
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyInvoiceServiceCategory)
	if err != nil {
		return InvoiceServiceCategoryDefault
	}
	if v := strings.TrimSpace(raw); v != "" {
		return v
	}
	return InvoiceServiceCategoryDefault
}
```

- [ ] **Step 3: 编译确认**

Run: `cd backend && go build ./internal/service/`
Expected: 成功

- [ ] **Step 4: 提交**

```bash
git add backend/internal/service/domain_constants.go backend/internal/service/setting_service.go
git commit -m "feat(settings): invoice fee rate + service category getters"
```

---

## Task 5: 把计算接入 CreateInvoiceRequest(TDD 配置解析)

**Files:**
- Modify: `backend/internal/service/invoice_amount.go`(加 PaymentService 配置解析 helper)
- Modify: `backend/internal/service/invoice_amount_test.go`(测 helper)
- Modify: `backend/internal/service/invoice_service.go`(`CreateInvoiceRequest` 约 484-494)

PaymentService 已有 `invoiceSettingService *SettingService`(`payment_service.go:192`,经 `wire_gen.go:244` 注入)。`invoice_email_body.go:14` 是 nil-guard 范例。

- [ ] **Step 1: 写失败测试(配置解析,nil-safe → 走默认值)**

在 `invoice_amount_test.go` 追加:

```go
func TestResolveInvoiceFeeConfig_DefaultsWhenNoSettingService(t *testing.T) {
	s := &PaymentService{} // invoiceSettingService 为 nil → 回退默认

	rate, category := s.resolveInvoiceFeeConfig(context.Background(), InvoiceTypeVATSpecial)
	require.Equal(t, InvoiceVATSpecialFeeRateDefault, rate)
	require.Equal(t, InvoiceServiceCategoryDefault, category)

	rate, category = s.resolveInvoiceFeeConfig(context.Background(), InvoiceTypeGeneral)
	require.Equal(t, 0.0, rate) // 普票费率恒为 0
	require.Equal(t, InvoiceServiceCategoryDefault, category)
}
```

并在该测试文件 import 块加入 `"context"`(若尚未存在)。

- [ ] **Step 2: 运行,确认失败**

Run: `cd backend && go test -tags unit ./internal/service/ -run 'TestResolveInvoiceFeeConfig' -v`
Expected: 编译失败 `s.resolveInvoiceFeeConfig undefined`

- [ ] **Step 3: 写 helper**

在 `invoice_amount.go` 追加(需 import `"context"`):

```go
// resolveInvoiceFeeConfig 解析本次申请适用的费率与类目。
// 仅专票适用费率;普票费率恒为 0。无 SettingService(如单测)时回退默认值。
func (s *PaymentService) resolveInvoiceFeeConfig(ctx context.Context, invoiceType string) (feeRate float64, category string) {
	rate := InvoiceVATSpecialFeeRateDefault
	category = InvoiceServiceCategoryDefault
	if s != nil && s.invoiceSettingService != nil {
		rate = s.invoiceSettingService.GetInvoiceVATSpecialFeeRate(ctx)
		category = s.invoiceSettingService.GetInvoiceServiceCategory(ctx)
	}
	if invoiceType == InvoiceTypeVATSpecial {
		feeRate = rate
	}
	return feeRate, category
}
```

更新 `invoice_amount.go` 的 import 为:

```go
import (
	"context"
	"math"
)
```

- [ ] **Step 4: 运行,确认通过**

Run: `cd backend && go test -tags unit ./internal/service/ -run 'TestResolveInvoiceFeeConfig' -v`
Expected: PASS

- [ ] **Step 5: 在 CreateInvoiceRequest 计算并写入新列**

在 `CreateInvoiceRequest` 中,把现有(约 484-487):

```go
	totalAmount := 0.0
	for _, order := range orders {
		totalAmount += order.PayAmount
	}
```

之后、`serialNo := generateInvoiceSerialNo()` 之前插入:

```go
	totalAmount = round2(totalAmount)
	feeRate, serviceCategory := s.resolveInvoiceFeeConfig(ctx, snapshot.InvoiceType)
	feeAmount, invoiceAmount := computeInvoiceAmounts(totalAmount, feeRate)
```

并把 INSERT(约 490-494)替换为:

```go
	row := tx.QueryRowContext(ctx, `
		INSERT INTO invoice_requests (user_id, profile_id, serial_no, status, profile_snapshot, total_amount, base_amount, fee_rate, fee_amount, invoice_amount, service_category)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9, $10, $11)
		RETURNING `+invoiceRequestColumns,
		userID, input.ProfileID, serialNo, InvoiceStatusPending, string(snapshotBytes),
		totalAmount, totalAmount, feeRate, feeAmount, invoiceAmount, serviceCategory)
```

(`total_amount` 与 `base_amount` 都写 `totalAmount` = 申请金额合计。)

- [ ] **Step 6: 全量编译 + 单测**

Run: `cd backend && go build ./... && go test -tags unit ./internal/service/ -run 'Invoice'`
Expected: 编译成功;Invoice 相关单测 PASS

- [ ] **Step 7: 提交**

```bash
git add backend/internal/service/invoice_amount.go backend/internal/service/invoice_amount_test.go backend/internal/service/invoice_service.go
git commit -m "feat(invoice): compute 专票 fee + net amount on request creation"
```

---

## Task 6: 通过 PublicSettings 下发费率与类目

**Files:**
- Modify: `backend/internal/service/settings_view.go`(`service.PublicSettings` struct 约 233)
- Modify: `backend/internal/service/setting_service.go`(`GetPublicSettings` keys 列表 + 返回映射;`PublicSettingsInjectionPayload` 约 1123;`GetPublicSettingsForInjection` builder 约 1182)
- Modify: `backend/internal/handler/dto/settings.go`(`dto.PublicSettings` struct 约 262)
- Modify: `backend/internal/handler/setting_handler.go`(service→dto 映射,`RiskControlEnabled` 之后,约 56)

新增 JSON 字段:`invoice_vat_special_fee_rate`(float64)、`invoice_service_category`(string)。schema-drift 测试(`dto/public_settings_injection_schema_test.go`)要求 dto 字段也出现在 InjectionPayload,故两处都要加。

- [ ] **Step 1: service.PublicSettings 加字段**

在 `settings_view.go` 的 `PublicSettings` struct 末尾(`RiskControlEnabled` 之后)追加:

```go
	// 发票:专票开票费率与开票类目
	InvoiceVATSpecialFeeRate float64 `json:"invoice_vat_special_fee_rate"`
	InvoiceServiceCategory   string  `json:"invoice_service_category"`
```

- [ ] **Step 2: GetPublicSettings 读取并映射**

在 `GetPublicSettings` 的 `keys := []string{ ... }` 列表末尾(`SettingKeyRiskControlEnabled,` 之后)加入:

```go
		SettingKeyInvoiceVATSpecialFeeRate,
		SettingKeyInvoiceServiceCategory,
```

在 `return &PublicSettings{ ... }` 的末尾(`RiskControlEnabled: ...` 之后)加入:

```go
		InvoiceVATSpecialFeeRate: s.GetInvoiceVATSpecialFeeRate(ctx),
		InvoiceServiceCategory:   s.GetInvoiceServiceCategory(ctx),
```

(直接复用 Task 4 的 getter,自带默认值与 clamp。)

- [ ] **Step 3: dto.PublicSettings 加字段**

在 `dto/settings.go` 的 `PublicSettings` struct 末尾(`RiskControlEnabled` 之后)追加:

```go
	InvoiceVATSpecialFeeRate float64 `json:"invoice_vat_special_fee_rate"`
	InvoiceServiceCategory   string  `json:"invoice_service_category"`
```

- [ ] **Step 4: setting_handler.go service→dto 映射**

在 `dto.PublicSettings{ ... }` 字面量末尾(`RiskControlEnabled: settings.RiskControlEnabled,` 之后)追加:

```go
		InvoiceVATSpecialFeeRate: settings.InvoiceVATSpecialFeeRate,
		InvoiceServiceCategory:   settings.InvoiceServiceCategory,
```

- [ ] **Step 5: InjectionPayload + builder 加字段(否则 schema-drift 测试失败)**

在 `PublicSettingsInjectionPayload` struct 末尾(`RiskControlEnabled` 之后)追加:

```go
	InvoiceVATSpecialFeeRate float64 `json:"invoice_vat_special_fee_rate"`
	InvoiceServiceCategory   string  `json:"invoice_service_category"`
```

在 `GetPublicSettingsForInjection` 的 `return &PublicSettingsInjectionPayload{ ... }` 末尾(`RiskControlEnabled: settings.RiskControlEnabled,` 之后)追加:

```go
		InvoiceVATSpecialFeeRate: settings.InvoiceVATSpecialFeeRate,
		InvoiceServiceCategory:   settings.InvoiceServiceCategory,
```

- [ ] **Step 6: 编译 + schema-drift 测试**

Run: `cd backend && go build ./... && go test -tags unit ./internal/handler/dto/ -run 'TestPublicSettingsInjectionPayload_SchemaDoesNotDrift' -v`
Expected: PASS(若失败会列出缺失字段名)

- [ ] **Step 7: 提交**

```bash
git add backend/internal/service/settings_view.go backend/internal/service/setting_service.go backend/internal/handler/dto/settings.go backend/internal/handler/setting_handler.go
git commit -m "feat(settings): expose invoice fee rate + category in public settings"
```

---

## Task 7: 前端类型 + store

**Files:**
- Modify: `frontend/src/types/invoice.ts`(`InvoiceRequest` 约 52-76)
- Modify: `frontend/src/types/index.ts`(`PublicSettings` 约 188-236)
- Modify: `frontend/src/stores/app.ts`(fallback 默认对象 约 352-362;新增 computed 并在 return 暴露)

- [ ] **Step 1: InvoiceRequest 加字段**

在 `frontend/src/types/invoice.ts` 的 `InvoiceRequest` 接口里,`total_amount: number` 之后追加:

```typescript
  base_amount: number
  fee_rate: number
  fee_amount: number
  invoice_amount: number
  service_category: string
```

- [ ] **Step 2: PublicSettings 加字段**

在 `frontend/src/types/index.ts` 的 `PublicSettings` 接口里(`affiliate_enabled: boolean` 之后)追加:

```typescript
  invoice_vat_special_fee_rate: number
  invoice_service_category: string
```

- [ ] **Step 3: store fallback 默认值**

在 `frontend/src/stores/app.ts` 的 fallback 默认对象里(`affiliate_enabled: false,` 之后)追加:

```typescript
        invoice_vat_special_fee_rate: 0.06,
        invoice_service_category: '技术服务费',
```

- [ ] **Step 4: store 暴露 computed**

在 `frontend/src/stores/app.ts` 中,仿照 `backendModeEnabled`(约 51 行)新增 computed:

```typescript
  const invoiceVatSpecialFeeRate = computed(() => cachedPublicSettings.value?.invoice_vat_special_fee_rate ?? 0.06)
  const invoiceServiceCategory = computed(() => cachedPublicSettings.value?.invoice_service_category ?? '技术服务费')
```

并在该 store 的 `return { ... }` 里加入 `invoiceVatSpecialFeeRate, invoiceServiceCategory,`(与 `backendModeEnabled` 同列)。

- [ ] **Step 5: 类型检查**

Run: `cd frontend && pnpm vue-tsc --noEmit -p tsconfig.app.json 2>&1 | head -20`
Expected: 无新增报错(若项目用别的 typecheck 脚本,见 `frontend/package.json` 的 `scripts`,改用对应命令如 `pnpm type-check`)

- [ ] **Step 6: 提交**

```bash
git add frontend/src/types/invoice.ts frontend/src/types/index.ts frontend/src/stores/app.ts
git commit -m "feat(invoice): frontend types + store for fee rate + category"
```

---

## Task 8: 用户发票页(提示 banner + 金额拆分)

**Files:**
- Modify: `frontend/src/views/user/InvoiceView.vue`

已知引用:`appStore = useAppStore()`(540)、`profiles = ref<InvoiceProfile[]>([])`(549)、`selectedProfileId = ref<number|null>`(553)、`selectedTotal`(623-625)、`formatCurrency`(508 导入)、选择区在模板约 294-303、记录金额展示在约 138。

- [ ] **Step 1: 加预览计算 computed(script 区,selectedTotal 之后)**

```typescript
const selectedProfile = computed(() =>
  profiles.value.find((p) => p.id === selectedProfileId.value) || null
)
const isVatSpecialSelected = computed(() => selectedProfile.value?.invoice_type === 'vat_special')
const previewFeeRate = computed(() => (isVatSpecialSelected.value ? appStore.invoiceVatSpecialFeeRate : 0))
const previewFeeAmount = computed(() => Math.round(selectedTotal.value * previewFeeRate.value * 100) / 100)
const previewInvoiceAmount = computed(() => Math.round((selectedTotal.value - previewFeeAmount.value) * 100) / 100)
const previewFeePercent = computed(() => Math.round(previewFeeRate.value * 1000) / 10) // 6
const previewNetPercent = computed(() => Math.round((1 - previewFeeRate.value) * 1000) / 10) // 94
```

- [ ] **Step 2: 在订单选择汇总区(模板约 303 行 selectedSummary 附近)插入提示 + 拆分**

在显示 `selectedSummary` 的容器内/下方插入:

```vue
<!-- 专票开票费显著提示 -->
<div
  v-if="isVatSpecialSelected"
  class="mt-3 rounded-lg border border-amber-300 bg-amber-50 p-3 text-sm text-amber-800 dark:border-amber-700/60 dark:bg-amber-900/20 dark:text-amber-200"
>
  <div class="font-semibold">⚠ {{ t('invoice.fee.noticeTitle') }}</div>
  <div class="mt-1">
    {{ t('invoice.fee.notice', {
      rate: previewFeePercent,
      net: previewNetPercent,
      category: appStore.invoiceServiceCategory,
    }) }}
  </div>
</div>

<!-- 金额拆分 -->
<div class="mt-3 space-y-1 text-sm">
  <div class="flex justify-between">
    <span class="text-gray-500 dark:text-gray-400">{{ t('invoice.fields.baseAmount') }}</span>
    <span>{{ formatCurrency(selectedTotal) }}</span>
  </div>
  <div v-if="isVatSpecialSelected" class="flex justify-between text-amber-700 dark:text-amber-300">
    <span>{{ t('invoice.fields.invoiceFee') }} ({{ previewFeePercent }}%)</span>
    <span>-{{ formatCurrency(previewFeeAmount) }}</span>
  </div>
  <div class="flex justify-between font-semibold border-t border-gray-200 dark:border-dark-700 pt-1">
    <span>{{ t('invoice.fields.invoiceAmount') }}</span>
    <span>{{ formatCurrency(previewInvoiceAmount) }}</span>
  </div>
  <div class="flex justify-between text-gray-500 dark:text-gray-400">
    <span>{{ t('invoice.fields.serviceCategory') }}</span>
    <span>{{ appStore.invoiceServiceCategory }}</span>
  </div>
</div>
```

- [ ] **Step 3: 记录列表展示实际开票金额(模板约 138 行)**

把 `{{ formatCurrency(request.total_amount) }}` 改为 `{{ formatCurrency(request.invoice_amount) }}`,并在其下补充专票拆分提示:

```vue
<div class="text-lg font-semibold text-gray-900 dark:text-white">{{ formatCurrency(request.invoice_amount) }}</div>
<div v-if="request.fee_amount > 0" class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
  {{ t('invoice.fields.baseAmount') }} {{ formatCurrency(request.base_amount) }} ·
  {{ t('invoice.fields.invoiceFee') }} -{{ formatCurrency(request.fee_amount) }}
</div>
```

- [ ] **Step 4: 类型检查 + 构建**

Run: `cd frontend && pnpm vue-tsc --noEmit -p tsconfig.app.json 2>&1 | head -20`
Expected: 无新增报错

- [ ] **Step 5: 提交**

```bash
git add frontend/src/views/user/InvoiceView.vue
git commit -m "feat(invoice): user page 专票 fee notice + amount breakdown"
```

---

## Task 9: 管理员发票页(拆分展示 + 完成开票提示)

**Files:**
- Modify: `frontend/src/views/admin/AdminInvoicesView.vue`(`completeForm` 约 307-310;total_amount 展示 约 119-120;完成开票对话框)

- [ ] **Step 1: 列表金额改为实际开票金额 + 拆分**

把列表里显示 `formatCurrency(item.total_amount)`(约 119)改为 `formatCurrency(item.invoice_amount)`,并在其下增加专票拆分:

```vue
<div class="text-lg font-semibold text-gray-900 dark:text-white">{{ formatCurrency(item.invoice_amount) }}</div>
<div v-if="item.fee_amount > 0" class="text-xs text-gray-500 dark:text-gray-400">
  {{ t('invoice.fields.baseAmount') }} {{ formatCurrency(item.base_amount) }} ·
  {{ t('invoice.fields.invoiceFee') }} -{{ formatCurrency(item.fee_amount) }} ·
  {{ t('invoice.fields.serviceCategory') }} {{ item.service_category }}
</div>
```

- [ ] **Step 2: 完成开票对话框顶部加显著提示**

在「完成开票」对话框(`completeForm` 绑定的对话框,约模板 173-234 行)的表单字段之前,插入(`currentItem` 为当前被操作的申请对象;若该组件用别名如 `selectedRequest`/`activeItem`,改用实际变量名——以对话框内已引用的当前项变量为准):

```vue
<div class="mb-3 rounded-lg border border-blue-300 bg-blue-50 p-3 text-sm text-blue-800 dark:border-blue-700/60 dark:bg-blue-900/20 dark:text-blue-200">
  {{ t('invoice.admin.completeAmountNotice', {
    amount: formatCurrency(currentItem?.invoice_amount ?? 0),
    category: currentItem?.service_category || t('invoice.fields.serviceCategory'),
  }) }}
</div>
```

- [ ] **Step 3: 类型检查**

Run: `cd frontend && pnpm vue-tsc --noEmit -p tsconfig.app.json 2>&1 | head -20`
Expected: 无新增报错

- [ ] **Step 4: 提交**

```bash
git add frontend/src/views/admin/AdminInvoicesView.vue
git commit -m "feat(invoice): admin page net amount + complete-dialog notice"
```

---

## Task 10: i18n

**Files:**
- Modify: `frontend/src/i18n/locales/zh.ts`(`invoice` 节 约 6928-7084)
- Modify: `frontend/src/i18n/locales/en.ts`(`invoice` 节 约 6748+)

- [ ] **Step 1: 中文新增键**

在 `zh.ts` 的 `invoice.fields` 对象内追加:

```typescript
    baseAmount: '申请金额',
    invoiceFee: '专票开票费',
    invoiceAmount: '实际开票金额',
    serviceCategory: '开票类目',
```

在 `invoice` 节内(与 `fields` 同级)新增 `fee` 子对象:

```typescript
  fee: {
    noticeTitle: '专票须知',
    notice: '增值税专用发票将收取 {rate}% 开票费,从开票金额中扣除。实际开票金额 = 申请金额 × {net}%,开票类目为{category}。',
  },
```

在 `invoice.admin` 对象内追加:

```typescript
    completeAmountNotice: '请按实际开票金额 {amount}、类目「{category}」开具增值税专用发票。',
```

- [ ] **Step 2: 英文对称新增键**

在 `en.ts` 的 `invoice.fields` 内追加:

```typescript
    baseAmount: 'Requested Amount',
    invoiceFee: 'VAT-Special Fee',
    invoiceAmount: 'Invoice Amount',
    serviceCategory: 'Category',
```

`invoice.fee` 子对象:

```typescript
  fee: {
    noticeTitle: 'VAT Special Invoice Notice',
    notice: 'A {rate}% fee applies to VAT special invoices and is deducted from the amount. Invoice amount = requested amount × {net}%; category: {category}.',
  },
```

`invoice.admin` 内追加:

```typescript
    completeAmountNotice: 'Please issue the VAT special invoice for {amount}, category "{category}".',
```

- [ ] **Step 3: 类型检查 + 构建确认 i18n 无缺键**

Run: `cd frontend && pnpm vue-tsc --noEmit -p tsconfig.app.json 2>&1 | head -20`
Expected: 无新增报错

- [ ] **Step 4: 提交**

```bash
git add frontend/src/i18n/locales/zh.ts frontend/src/i18n/locales/en.ts
git commit -m "i18n(invoice): fee + category strings (zh/en)"
```

---

## Task 11: 全量验证

- [ ] **Step 1: 后端编译 + 单测**

Run: `cd backend && go build ./... && go test -tags unit ./internal/service/ ./internal/handler/dto/ ./internal/handler/...`
Expected: 全部 PASS

- [ ] **Step 2: 前端类型检查 + 构建**

Run: `cd frontend && pnpm vue-tsc --noEmit -p tsconfig.app.json && pnpm build 2>&1 | tail -15`
Expected: 构建成功(若 `pnpm build` 脚本名不同,见 `frontend/package.json`)

- [ ] **Step 3: 人工冒烟(可选,用 verify/run 技能或本地起服务)**
  - 新建专票抬头 → 选订单(¥1000)→ 看到 banner 与拆分(申请 1000 / 费 -60 / 实际 940 / 类目 技术服务费)。
  - 提交后在「开票记录」看到实际开票金额 940。
  - 管理员页看到拆分与「请按 ¥940.00 开具」提示。
  - 普票抬头:无 banner、无费用行、金额 1000。

- [ ] **Step 4: 最终提交(如有零散改动)**

```bash
git add -A && git commit -m "chore(invoice): finalize 专票 fee feature" || echo "nothing to commit"
```

---

## Self-Review 备注(已核对)

- **Spec 覆盖**:数据模型(T1/T3)、计算与精度(T2/T5)、设置项(T4)、公共设置下发(T6)、用户页提示+拆分(T8)、管理员页(T9)、i18n(T10)、测试(T2/T5/T11)——逐项有任务对应。
- **类型一致**:`base_amount/fee_rate/fee_amount/invoice_amount/service_category` 在 Go struct、列常量、scan、INSERT、TS 类型、Vue 使用处命名一致;getter/常量名 `GetInvoiceVATSpecialFeeRate`、`InvoiceVATSpecialFeeRateDefault`、`resolveInvoiceFeeConfig` 前后一致。
- **迁移号**:用 145(>仓库现有最高 144)。
- **风险点**:管理员完成开票对话框的「当前项」变量名以组件实际为准(T9 Step2 已注明);前端 typecheck 脚本名以 `frontend/package.json` 为准(T7 已注明)。
