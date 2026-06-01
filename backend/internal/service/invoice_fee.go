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
