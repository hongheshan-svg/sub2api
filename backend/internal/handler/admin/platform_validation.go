package admin

import (
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
)

// 平台相关的请求绑定校验标签，由平台清单（domain/platforms.go）驱动，替代在
// binding 标签里手写 oneof 白名单（新增平台时易漏加，导致分组无法创建）：
//   - group_platform：已登记的具体平台或 composite；
//   - concrete_platform：已登记的具体平台（仅用于组合路由目标；fork：不含 kiro）。
func init() {
	engine, ok := binding.Validator.Engine().(*validator.Validate)
	if !ok {
		panic("platform validation requires a go-playground validator engine")
	}
	if err := engine.RegisterValidation("group_platform", func(fl validator.FieldLevel) bool {
		return domain.IsGroupPlatform(fl.Field().String())
	}); err != nil {
		panic(fmt.Errorf("register group_platform validation: %w", err))
	}
	if err := engine.RegisterValidation("concrete_platform", func(fl validator.FieldLevel) bool {
		return domain.IsCompositeMemberPlatform(fl.Field().String())
	}); err != nil {
		panic(fmt.Errorf("register concrete_platform validation: %w", err))
	}
}
