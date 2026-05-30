package service

import (
	"context"
	"math"
)

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
