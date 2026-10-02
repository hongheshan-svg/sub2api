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
		// Chained proxies may send "https, http"; take the first value.
		scheme = strings.TrimSpace(strings.Split(proto, ",")[0])
	} else if c.Request.TLS == nil {
		scheme = "http"
	}
	if host := c.Request.Host; host != "" {
		return scheme + "://" + host
	}
	return ""
}

func llmsInput(c *gin.Context, settingService *service.SettingService) web.LLMsInput {
	ctx := c.Request.Context()
	return web.LLMsInput{
		SiteName:     settingService.GetSiteName(ctx),
		SiteSubtitle: settingService.GetSiteSubtitle(ctx),
		BaseURL:      resolveBaseURL(c, settingService),
		DocURL:       settingService.GetDocURL(ctx),
		Endpoints:    web.ParseLLMsEndpoints(settingService.GetCustomEndpoints(ctx)),
	}
}

// RegisterSEORoutes 注册 /robots.txt /sitemap.xml /llms.txt /llms-full.txt /site-logo。
func RegisterSEORoutes(r *gin.Engine, settingService *service.SettingService) {
	landing := web.LandingPages() // nil in non-embed builds -> sitemap falls back to "/"

	r.GET("/robots.txt", func(c *gin.Context) {
		base := resolveBaseURL(c, settingService)
		c.Header("Cache-Control", "public, max-age=3600")
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(web.BuildRobotsTxt(base)))
	})

	r.GET("/sitemap.xml", func(c *gin.Context) {
		base := resolveBaseURL(c, settingService)
		c.Header("Cache-Control", "public, max-age=3600")
		c.Data(http.StatusOK, "application/xml; charset=utf-8", []byte(web.BuildSitemapXML(base, landing)))
	})

	r.GET("/llms.txt", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=3600")
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(web.BuildLLMsTxt(llmsInput(c, settingService), landing)))
	})

	r.GET("/llms-full.txt", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=3600")
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(web.BuildLLMsFullTxt(llmsInput(c, settingService), landing)))
	})

	// Admin-uploaded logos are stored as data: URIs; the HTML references them
	// via web.SiteLogoHref so pages stay small and og:image is a real URL.
	r.GET(web.SiteLogoPath, func(c *gin.Context) {
		logo := settingService.GetSiteLogo(c.Request.Context())
		contentType, data, ok := web.DecodeDataImage(logo)
		if !ok {
			c.Status(http.StatusNotFound)
			return
		}
		if c.Query("v") == web.SiteLogoVersion(logo) {
			c.Header("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			c.Header("Cache-Control", "public, max-age=300")
		}
		// The image is admin-supplied; never let a (SVG) logo run as a document.
		c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Data(http.StatusOK, contentType, data)
	})
}
