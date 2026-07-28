package routes

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAuthenticatedPaymentGroupsApplyPanelRateLimit 守卫面板限流在支付/发票路由上的接线。
//
// 背景：RegisterPaymentRoutes 的调用点(server/router.go)在与上游同步时反复冲突,
// fork 的发票参数与上游新增的 panelRateLimiter 参数正落在同一行。限流一旦被漏掉,
// 运行时不会报任何错——只是这些接口失去了数据库保护。这里按源码扫描兜底:
// 凡是走 jwtAuth 的用户侧分组,都必须挂上按用户限流。
//
// 管理端分组(adminAuth)不在此列——上游未对其接线,且管理员默认豁免该限流。
func TestAuthenticatedPaymentGroupsApplyPanelRateLimit(t *testing.T) {
	source, err := os.ReadFile("payment.go")
	require.NoError(t, err)
	routeSource := string(source)

	groupPattern := regexp.MustCompile(`(\w+) := v1\.Group\("([^"]+)"\)`)
	groups := groupPattern.FindAllStringSubmatch(routeSource, -1)
	require.NotEmpty(t, groups, "no route groups found; payment.go layout changed")

	authenticated := make([]string, 0, len(groups))
	missing := make([]string, 0)
	for _, group := range groups {
		name, prefix := group[1], group[2]
		if !strings.Contains(routeSource, fmt.Sprintf("%s.Use(gin.HandlerFunc(jwtAuth))", name)) {
			continue
		}
		authenticated = append(authenticated, prefix)
		if !strings.Contains(routeSource, fmt.Sprintf("%s.Use(panelRateLimiter.Global())", name)) {
			missing = append(missing, prefix)
		}
	}

	sort.Strings(authenticated)
	sort.Strings(missing)
	require.Equal(t, []string{"/invoice", "/payment"}, authenticated,
		"authenticated group set changed; keep this guard in sync with payment.go")
	require.Empty(t, missing,
		"every jwtAuth-protected payment group must apply panelRateLimiter.Global()")
}
