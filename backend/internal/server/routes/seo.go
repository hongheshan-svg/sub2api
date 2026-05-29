package routes

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/web"
	"github.com/gin-gonic/gin"
)

// resolveBaseURL 优先用配置的站点 URL,空则用请求 scheme://host 兜底。
func resolveBaseURL(c *gin.Context, settingService *service.SettingService) string {
	if u := strings.TrimSpace(settingService.GetFrontendURL(c.Request.Context())); u != "" {
		return strings.TrimRight(u, "/")
	}
	scheme := "https"
	if proto := c.GetHeader("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if c.Request.TLS == nil {
		scheme = "http"
	}
	if host := c.Request.Host; host != "" {
		return scheme + "://" + host
	}
	return ""
}

// RegisterSEORoutes 注册 /robots.txt /sitemap.xml /llms.txt。
func RegisterSEORoutes(r *gin.Engine, settingService *service.SettingService) {
	r.GET("/robots.txt", func(c *gin.Context) {
		base := resolveBaseURL(c, settingService)
		c.Header("Cache-Control", "public, max-age=3600")
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(web.BuildRobotsTxt(base)))
	})

	r.GET("/sitemap.xml", func(c *gin.Context) {
		base := resolveBaseURL(c, settingService)
		c.Header("Cache-Control", "public, max-age=3600")
		c.Data(http.StatusOK, "application/xml; charset=utf-8", []byte(web.BuildSitemapXML(base)))
	})

	r.GET("/llms.txt", func(c *gin.Context) {
		ctx := c.Request.Context()
		base := resolveBaseURL(c, settingService)
		c.Header("Cache-Control", "public, max-age=3600")
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(web.BuildLLMsTxt(web.LLMsInput{
			SiteName:     settingService.GetSiteName(ctx),
			SiteSubtitle: settingService.GetSiteSubtitle(ctx),
			BaseURL:      base,
			DocURL:       settingService.GetDocURL(ctx),
		})))
	})
}
