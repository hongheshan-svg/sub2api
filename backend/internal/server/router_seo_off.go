//go:build !embed

package server

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/web"
	"github.com/gin-gonic/gin"
)

// registerSEOEndpoints is a no-op for non-embed builds (SEO requires the embedded
// frontend's seo.Registry, which is not compiled into non-embed binaries).
func registerSEOEndpoints(_ *gin.Engine, _ *web.FrontendServer, _ *service.SettingService, _ string) {
}
