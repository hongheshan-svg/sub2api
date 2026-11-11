package service

import "time"

// LastUsedTouchDue 报告此刻调用 TouchLastUsed 是否有可能真的落库（防抖窗口已过，或该 key
// 尚未被 touch 过）。只读一次 L1 防抖表，不加锁、不分配。
//
// fork：API Key 鉴权中间件把 TouchLastUsed 挪到了独立 goroutine（不占用请求关键路径），
// 但防抖窗口内绝大多数调用是空操作——用它先判断，就不必为每个请求都起一个 goroutine 再建
// 一个带定时器的 context。并发下可能有多个请求同时判定为"到期"，由 TouchLastUsed 内部的
// singleflight + 二次检查收敛，语义与直接调用 TouchLastUsed 一致。
func (s *APIKeyService) LastUsedTouchDue(keyID int64) bool {
	if s == nil || keyID <= 0 {
		return false
	}
	if v, ok := s.lastUsedTouchL1.Load(keyID); ok {
		if nextAllowedAt, ok := v.(time.Time); ok && time.Now().Before(nextAllowedAt) {
			return false
		}
	}
	return true
}
