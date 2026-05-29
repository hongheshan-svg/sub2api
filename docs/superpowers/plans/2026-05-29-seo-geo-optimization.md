# 精简版 SEO / GEO 优化 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让搜索引擎与生成式 AI 引擎能发现并正确理解本系统:补齐 index.html 的 meta/OG/JSON-LD,并新增动态 `/robots.txt`、`/sitemap.xml`、`/llms.txt`(放行 AI 爬虫),全部基于部署配置的站点 URL/名称。

**Architecture:** 复用 Go `web.FrontendServer.injectSettings()` 的 `</head>` 注入接缝,新增纯函数生成 SEO head 与三个文本端点;端点经 `shouldBypassEmbeddedFrontend` 放行后由 `registerRoutes` 注册的 GET handler 提供,绝对 URL 来自 `GetFrontendURL`(空则用请求 Host 兜底)。

**Tech Stack:** Go (gin, encoding/json 默认 HTML 转义保证 JSON-LD 安全), Vue 3 SPA (index.html), `//go:build unit` 测试。

参考 spec:`docs/superpowers/specs/2026-05-29-seo-geo-optimization-design.md`

---

## File Structure

- `backend/internal/web/seo.go`(新)— 纯函数:`SEOInput`/`LLMsInput` 类型、`BuildSEOHead`、`BuildRobotsTxt`、`BuildSitemapXML`、`BuildLLMsTxt`、`absURL` 辅助。
- `backend/internal/web/seo_test.go`(新,`//go:build unit`)— 上述纯函数的单元测试。
- `frontend/index.html`(改)— 静态基线 meta。
- `backend/internal/web/embed_on.go`(改)— `injectSettings` 解析设置并注入 SEO head;`shouldBypassEmbeddedFrontend` 放行三路径。
- `backend/internal/service/setting_service.go`(改)— `PublicSettingsInjectionPayload` 增 `frontend_url` 字段并在 `GetPublicSettingsForInjection` 填充。
- `backend/internal/server/routes/seo.go`(新)— `RegisterSEORoutes(r, settingService)` 注册三个 GET handler。
- `backend/internal/server/router.go`(改)— 在 `registerRoutes` 调用 `routes.RegisterSEORoutes`。

---

## Task 1: index.html 静态基线 meta

**Files:**
- Modify: `frontend/index.html`

- [ ] **Step 1: 在 `<head>` 内补充 meta**

把现有 head(`<title>Sub2API - AI API Gateway</title>` 之后)插入以下标签:

```html
    <meta name="description" content="统一接入 Claude、GPT、Gemini 等大模型 API,兼容主流 IDE 插件与 CLI 工具,只需替换 base_url、30 秒完成接入的企业级 AI 编码中转网关。" />
    <meta name="keywords" content="AI API, Claude API, GPT API, Gemini API, API 网关, API 中转, AI 编码, LLM 网关" />
    <meta name="robots" content="index, follow" />
    <meta name="theme-color" content="#1a1a2e" />
    <meta property="og:type" content="website" />
    <meta property="og:image" content="/logo.png" />
    <meta name="twitter:card" content="summary_large_image" />
```

(生产环境服务端会再注入 canonical/og:url/og:title/og:description/JSON-LD;这些静态标签是开发/未注入时的基线。)

- [ ] **Step 2: 构建确认**

Run: `cd frontend && node_modules/.bin/vue-tsc --noEmit && node_modules/.bin/vite build 2>&1 | tail -3`
Expected: 构建成功(index.html 不影响 TS;vite 会把它输出到 dist)

- [ ] **Step 3: 提交**

```bash
git add frontend/index.html
git commit -m "feat(seo): static baseline meta tags in index.html"
```

---

## Task 2: SEO 纯函数(TDD)

**Files:**
- Create: `backend/internal/web/seo.go`
- Test: `backend/internal/web/seo_test.go`

纯函数无 I/O,易测。`encoding/json` 默认对 `<`/`>`/`&` 转义为 `<` 等,JSON-LD 中的 `</script>` 因此自动安全;meta 属性值用 `html.EscapeString` 转义。

- [ ] **Step 1: 写失败测试**

`backend/internal/web/seo_test.go`:

```go
//go:build unit

package web

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildSEOHead_WithBaseURL(t *testing.T) {
	head := string(BuildSEOHead(SEOInput{
		SiteName:     "GW-LINK",
		SiteSubtitle: "AI 网关",
		BaseURL:      "https://gw.example.com/",
		Logo:         "/logo.png",
		Lang:         "zh-CN",
	}))
	require.Contains(t, head, `<link rel="canonical" href="https://gw.example.com/"`)
	require.Contains(t, head, `property="og:url" content="https://gw.example.com/"`)
	require.Contains(t, head, `property="og:title" content="GW-LINK`)
	require.Contains(t, head, `property="og:image" content="https://gw.example.com/logo.png"`)
	require.Contains(t, head, `application/ld+json`)
	require.Contains(t, head, `"@type":"Organization"`)
	require.Contains(t, head, `"@type":"WebSite"`)
	require.Contains(t, head, `"@type":"SoftwareApplication"`)
	// JSON-LD 不得包含裸 </script>(json.Marshal 会转义为 <)
	require.NotContains(t, head, "</script></script>")
}

func TestBuildSEOHead_NoBaseURL_OmitsAbsoluteURLs(t *testing.T) {
	head := string(BuildSEOHead(SEOInput{SiteName: "GW-LINK", Logo: "/logo.png", Lang: "zh-CN"}))
	require.NotContains(t, head, `rel="canonical"`)
	require.NotContains(t, head, `og:url`)
	// 仍输出标题类标签与 JSON-LD
	require.Contains(t, head, `property="og:title"`)
	require.Contains(t, head, `"@type":"Organization"`)
}

func TestBuildSEOHead_EscapesSiteName(t *testing.T) {
	head := string(BuildSEOHead(SEOInput{SiteName: `A<b>"x`, BaseURL: "https://x.io", Lang: "zh-CN"}))
	require.NotContains(t, head, `A<b>"x`)            // 原始未转义串不得出现在属性里
	require.Contains(t, head, "A&lt;b&gt;")           // HTML 转义后的形式
}

func TestBuildRobotsTxt(t *testing.T) {
	r := BuildRobotsTxt("https://gw.example.com")
	for _, ua := range []string{"GPTBot", "ClaudeBot", "PerplexityBot", "Google-Extended", "CCBot"} {
		require.Contains(t, r, "User-agent: "+ua)
	}
	require.Contains(t, r, "Disallow: /admin")
	require.Contains(t, r, "Disallow: /api/")
	require.Contains(t, r, "Sitemap: https://gw.example.com/sitemap.xml")
}

func TestBuildRobotsTxt_NoBaseURL_RelativeSitemap(t *testing.T) {
	require.Contains(t, BuildRobotsTxt(""), "Sitemap: /sitemap.xml")
}

func TestBuildSitemapXML(t *testing.T) {
	x := BuildSitemapXML("https://gw.example.com")
	require.True(t, strings.HasPrefix(x, `<?xml version="1.0" encoding="UTF-8"?>`))
	require.Contains(t, x, "<loc>https://gw.example.com/</loc>")
	require.Contains(t, x, "<loc>https://gw.example.com/home</loc>")
	require.Contains(t, x, "<loc>https://gw.example.com/login</loc>")
	require.Contains(t, x, "</urlset>")
}

func TestBuildLLMsTxt(t *testing.T) {
	out := BuildLLMsTxt(LLMsInput{SiteName: "GW-LINK", SiteSubtitle: "AI 网关", BaseURL: "https://gw.example.com", DocURL: "https://docs.example.com"})
	require.Contains(t, out, "# GW-LINK")
	require.Contains(t, out, "> AI 网关")
	require.Contains(t, out, "https://gw.example.com/")
	require.Contains(t, out, "https://docs.example.com")
}

func TestBuildLLMsTxt_NoDocURL_OmitsDocLine(t *testing.T) {
	out := BuildLLMsTxt(LLMsInput{SiteName: "GW-LINK", BaseURL: "https://gw.example.com"})
	require.NotContains(t, out, "接入文档")
}
```

- [ ] **Step 2: 运行,确认失败**

Run: `cd backend && go test -tags unit ./internal/web/ -run 'SEO|Robots|Sitemap|LLMs' -v`
Expected: 编译失败(`undefined: BuildSEOHead` 等)

- [ ] **Step 3: 写实现**

`backend/internal/web/seo.go`:

```go
package web

import (
	"encoding/json"
	"html"
	"strings"
)

// SEOInput 是构建注入到 <head> 的 SEO 片段所需的输入。
type SEOInput struct {
	SiteName     string
	SiteSubtitle string
	BaseURL      string // 站点绝对 URL,可能为空
	Logo         string // logo 路径或绝对 URL
	Lang         string // 如 "zh-CN"
}

// LLMsInput 是构建 /llms.txt 所需的输入。
type LLMsInput struct {
	SiteName     string
	SiteSubtitle string
	BaseURL      string
	DocURL       string
}

// trimSlash 去掉结尾的 /。
func trimSlash(s string) string { return strings.TrimRight(strings.TrimSpace(s), "/") }

// absURL 将 path 拼成绝对 URL;path 已是绝对(http)则原样返回;base 为空则返回 path。
func absURL(base, path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	base = trimSlash(base)
	if base == "" {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}

// jsonLD 把 v 序列化为 JSON(encoding/json 默认转义 <、>、&,故 </script> 安全)。
func jsonLD(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// BuildSEOHead 返回要插入 </head> 前的 SEO 标签片段。
func BuildSEOHead(in SEOInput) []byte {
	name := strings.TrimSpace(in.SiteName)
	if name == "" {
		name = "Sub2API"
	}
	desc := strings.TrimSpace(in.SiteSubtitle)
	if desc == "" {
		desc = "统一接入 Claude、GPT、Gemini 等大模型 API 的 AI 编码中转网关。"
	}
	base := trimSlash(in.BaseURL)
	logo := strings.TrimSpace(in.Logo)
	if logo == "" {
		logo = "/logo.png"
	}
	title := name + " - AI API Gateway"

	e := html.EscapeString
	var b strings.Builder
	b.WriteString("\n")
	if base != "" {
		b.WriteString(`<link rel="canonical" href="` + e(base+"/") + `" />` + "\n")
		b.WriteString(`<meta property="og:url" content="` + e(base+"/") + `" />` + "\n")
	}
	b.WriteString(`<meta property="og:site_name" content="` + e(name) + `" />` + "\n")
	b.WriteString(`<meta property="og:title" content="` + e(title) + `" />` + "\n")
	b.WriteString(`<meta property="og:description" content="` + e(desc) + `" />` + "\n")
	b.WriteString(`<meta property="og:image" content="` + e(absURL(base, logo)) + `" />` + "\n")
	b.WriteString(`<meta name="twitter:title" content="` + e(title) + `" />` + "\n")
	b.WriteString(`<meta name="twitter:description" content="` + e(desc) + `" />` + "\n")

	siteURL := base + "/"
	if base == "" {
		siteURL = "/"
	}
	org := map[string]any{"@context": "https://schema.org", "@type": "Organization", "name": name, "url": siteURL, "logo": absURL(base, logo)}
	site := map[string]any{"@context": "https://schema.org", "@type": "WebSite", "name": name, "url": siteURL}
	app := map[string]any{"@context": "https://schema.org", "@type": "SoftwareApplication", "name": name, "applicationCategory": "DeveloperApplication", "operatingSystem": "Any", "description": desc}
	for _, ld := range []any{org, site, app} {
		b.WriteString(`<script type="application/ld+json">` + jsonLD(ld) + `</script>` + "\n")
	}
	return []byte(b.String())
}

// BuildRobotsTxt 生成 robots.txt 内容;放行主流 AI 爬虫。
func BuildRobotsTxt(baseURL string) string {
	base := trimSlash(baseURL)
	sitemap := "/sitemap.xml"
	if base != "" {
		sitemap = base + "/sitemap.xml"
	}
	aiBots := []string{"GPTBot", "ClaudeBot", "PerplexityBot", "Google-Extended", "CCBot"}
	var b strings.Builder
	b.WriteString("User-agent: *\n")
	b.WriteString("Allow: /\n")
	for _, d := range []string{"/admin", "/dashboard", "/api/", "/v1/", "/backend-api/"} {
		b.WriteString("Disallow: " + d + "\n")
	}
	b.WriteString("\n# AI crawlers (GEO)\n")
	for _, ua := range aiBots {
		b.WriteString("User-agent: " + ua + "\nAllow: /\n")
	}
	b.WriteString("\nSitemap: " + sitemap + "\n")
	return b.String()
}

// BuildSitemapXML 生成 sitemap.xml,列出公开营销路由。
func BuildSitemapXML(baseURL string) string {
	base := trimSlash(baseURL)
	type u struct {
		path, freq, prio string
	}
	urls := []u{{"/", "weekly", "1.0"}, {"/home", "weekly", "0.9"}, {"/login", "monthly", "0.5"}}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	for _, x := range urls {
		loc := x.path
		if base != "" {
			loc = base + x.path
		}
		b.WriteString("  <url><loc>" + loc + "</loc><changefreq>" + x.freq + "</changefreq><priority>" + x.prio + "</priority></url>\n")
	}
	b.WriteString("</urlset>\n")
	return b.String()
}

// BuildLLMsTxt 生成 /llms.txt(GEO),markdown 约定格式。
func BuildLLMsTxt(in LLMsInput) string {
	name := strings.TrimSpace(in.SiteName)
	if name == "" {
		name = "Sub2API"
	}
	subtitle := strings.TrimSpace(in.SiteSubtitle)
	if subtitle == "" {
		subtitle = "AI API 网关 / 中转平台"
	}
	base := trimSlash(in.BaseURL)
	home := base + "/"
	login := base + "/login"
	if base == "" {
		home, login = "/", "/login"
	}
	var b strings.Builder
	b.WriteString("# " + name + "\n\n")
	b.WriteString("> " + subtitle + "\n\n")
	b.WriteString(name + " 是一个 AI API 网关 / 中转平台,统一接入 Claude、GPT、Gemini 等大模型,兼容主流 IDE 插件与 CLI 工具,只需替换 base_url 即可接入。\n\n")
	b.WriteString("## 主要页面\n")
	b.WriteString("- [首页](" + home + "): 产品介绍与定价\n")
	b.WriteString("- [登录](" + login + "): 用户登录/注册\n")
	if doc := strings.TrimSpace(in.DocURL); doc != "" {
		b.WriteString("- [接入文档](" + doc + "): API 接入说明\n")
	}
	return b.String()
}
```

- [ ] **Step 4: 运行,确认通过**

Run: `cd backend && go test -tags unit ./internal/web/ -run 'SEO|Robots|Sitemap|LLMs' -v`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/web/seo.go backend/internal/web/seo_test.go
git commit -m "feat(seo): pure builders for SEO head + robots/sitemap/llms"
```

---

## Task 3: 注入 SEO head + 公共设置增 frontend_url

**Files:**
- Modify: `backend/internal/service/setting_service.go`(`PublicSettingsInjectionPayload` struct + `GetPublicSettingsForInjection` 返回字面量)
- Modify: `backend/internal/web/embed_on.go`(`injectSettings` 约 193-206)

说明:`frontend_url` 仅加到 `PublicSettingsInjectionPayload`(注入用),**不**加到 `dto.PublicSettings` —— schema-drift 测试只校验「dto 有但 injection 没有」的字段,injection 多出字段是允许的,故无需改 dto。

- [ ] **Step 1: 给注入 payload 增 frontend_url 字段**

在 `PublicSettingsInjectionPayload` struct(`setting_service.go`)末尾、`RiskControlEnabled` 等字段之后追加:

```go
	FrontendURL string `json:"frontend_url"`
```

- [ ] **Step 2: 在 GetPublicSettingsForInjection 填充**

在 `return &PublicSettingsInjectionPayload{ ... }` 字面量里(任意位置,建议靠近 `SiteName:` 行)加入:

```go
		FrontendURL: s.GetFrontendURL(ctx),
```

(`GetFrontendURL` 已存在:读 `frontend_url` 设置,空则回退 `cfg.Server.FrontendURL`。)

- [ ] **Step 3: 在 injectSettings 注入 SEO head**

把 `embed_on.go` 的 `injectSettings`(约 193-206)替换为:

```go
func (s *FrontendServer) injectSettings(settingsJSON []byte) []byte {
	// Create the script tag to inject with nonce placeholder
	// The placeholder will be replaced with actual nonce at request time
	script := []byte(`<script nonce="` + NonceHTMLPlaceholder + `">window.__APP_CONFIG__=` + string(settingsJSON) + `;</script>`)

	// Build the SEO head block from the injected settings.
	var cfg struct {
		SiteName     string `json:"site_name"`
		SiteSubtitle string `json:"site_subtitle"`
		SiteLogo     string `json:"site_logo"`
		FrontendURL  string `json:"frontend_url"`
	}
	_ = json.Unmarshal(settingsJSON, &cfg)
	seoHead := BuildSEOHead(SEOInput{
		SiteName:     cfg.SiteName,
		SiteSubtitle: cfg.SiteSubtitle,
		BaseURL:      cfg.FrontendURL,
		Logo:         cfg.SiteLogo,
		Lang:         "zh-CN",
	})

	// Inject SEO head + config script before </head>
	headClose := []byte("</head>")
	inject := append(seoHead, script...)
	result := bytes.Replace(s.baseHTML, headClose, append(inject, headClose...), 1)

	// Replace <title> with custom site name so the browser tab shows it immediately
	result = injectSiteTitle(result, settingsJSON)

	return result
}
```

(`encoding/json` 已在该文件 import;`BuildSEOHead`/`SEOInput` 同包。)

- [ ] **Step 4: 构建 + schema-drift 测试**

Run: `cd backend && go build ./... && go test -tags unit ./internal/handler/dto/ -run 'SchemaDoesNotDrift' && go test -tags unit ./internal/web/ -run 'SEO'`
Expected: 编译成功;两个测试 PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/service/setting_service.go backend/internal/web/embed_on.go
git commit -m "feat(seo): inject SEO head into served index.html; expose frontend_url for injection"
```

---

## Task 4: 端点路由(robots/sitemap/llms)+ 放行

**Files:**
- Modify: `backend/internal/web/embed_on.go`(`shouldBypassEmbeddedFrontend` 约 291-303)
- Create: `backend/internal/server/routes/seo.go`
- Modify: `backend/internal/server/router.go`(`registerRoutes`,`routes.RegisterCommonRoutes(r)` 之后)

- [ ] **Step 1: 放行三路径(否则被 SPA catch-all 吞掉)**

在 `shouldBypassEmbeddedFrontend` 的 return 表达式中,`strings.HasPrefix(trimmed, "/images/")` 之后追加(注意前一行补 `||`):

```go
		strings.HasPrefix(trimmed, "/images/") ||
		trimmed == "/robots.txt" ||
		trimmed == "/sitemap.xml" ||
		trimmed == "/llms.txt"
```

- [ ] **Step 2: 新建 SEO 路由文件**

`backend/internal/server/routes/seo.go`:

```go
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
			SiteSubtitle: strings.TrimSpace(settingService.GetSiteName(ctx)), // 占位,下一行覆盖
			BaseURL:      base,
			DocURL:       settingService.GetDocURL(ctx),
		})))
	})
}
```

> 注意:`SiteSubtitle` 与 `DocURL` 需要 getter。`GetSiteName` 已存在。若 `GetDocURL` / `GetSiteSubtitle` 不存在,见 Step 3 先确认并按需补最小 getter。

- [ ] **Step 3: 确认/补齐 getter(GetDocURL / GetSiteSubtitle）**

Run: `cd backend && grep -nE "func \(s \*SettingService\) (GetDocURL|GetSiteSubtitle)\(" internal/service/setting_service.go`

若**存在**:把 Step 2 里 `SiteSubtitle:` 行改为 `settingService.GetSiteSubtitle(ctx)`,`DocURL:` 用 `settingService.GetDocURL(ctx)`。

若**不存在**:在 `setting_service.go` 仿照 `GetSiteName`(约 2524-2531)新增:

```go
func (s *SettingService) GetSiteSubtitle(ctx context.Context) string {
	value, err := s.settingRepo.GetValue(ctx, SettingKeySiteSubtitle)
	if err != nil || strings.TrimSpace(value) == "" {
		return "Subscription to API Conversion Platform"
	}
	return value
}

func (s *SettingService) GetDocURL(ctx context.Context) string {
	value, _ := s.settingRepo.GetValue(ctx, SettingKeyDocURL)
	return strings.TrimSpace(value)
}
```

然后把 Step 2 的 `llms.txt` handler 改为:

```go
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
```

(`strings` / `context` 已在 setting_service.go import。)

- [ ] **Step 4: 在 registerRoutes 注册**

在 `router.go` 的 `registerRoutes` 中,`routes.RegisterCommonRoutes(r)`(约 104 行)之后追加:

```go
	routes.RegisterSEORoutes(r, settingService)
```

- [ ] **Step 5: 构建 + 端点冒烟(无需起服务,用 httptest)**

Run: `cd backend && go build ./...`
Expected: 编译成功

(可选)用现有路由测试风格验证;若无,跳过——Task 5 做整体验证。

- [ ] **Step 6: 提交**

```bash
git add backend/internal/web/embed_on.go backend/internal/server/routes/seo.go backend/internal/server/router.go backend/internal/service/setting_service.go
git commit -m "feat(seo): serve /robots.txt /sitemap.xml /llms.txt with base-URL resolution"
```

---

## Task 5: 全量验证

- [ ] **Step 1: 后端 build + lint + 单测**

Run:
```bash
cd backend && go build ./... \
 && "$(go env GOPATH)/bin/golangci-lint" run --max-same-issues 0 \
 && go test -tags unit ./internal/web/ ./internal/handler/dto/ ./internal/service/
```
Expected: build 成功;golangci-lint `0 issues`;测试全 PASS
(若 golangci-lint 未安装:`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.9.0`)

- [ ] **Step 2: 前端 typecheck + 构建(含 index.html 注入到 dist)**

Run: `cd frontend && node_modules/.bin/vue-tsc --noEmit && node_modules/.bin/vite build 2>&1 | tail -3`
Expected: 成功

- [ ] **Step 3: 端点 httptest 冒烟(可选,起内嵌服务需 -tags embed)**

人工验证(若本地可起服务):
```bash
curl -s localhost:PORT/robots.txt | grep -E "GPTBot|Sitemap"
curl -s localhost:PORT/sitemap.xml | head -3
curl -s localhost:PORT/llms.txt | head -5
curl -s localhost:PORT/ | grep -E "og:title|application/ld\+json|canonical"
```
Expected:robots 含 AI 爬虫与 Sitemap;sitemap 合法;llms 含站点名;首页 HTML 含 OG/JSON-LD/canonical。

- [ ] **Step 4: 收尾提交(如有零散改动)**

```bash
git add -A && git commit -m "chore(seo): finalize SEO/GEO endpoints" || echo "nothing to commit"
```

---

## Self-Review 备注(已核对)

- **Spec 覆盖**:§4.1 index.html(T1)、§4.2 SEO head + 注入(T2/T3)、§4.3 robots(T2/T4)、§4.4 sitemap(T2/T4)、§4.5 llms(T2/T4)、§4.6 放行+注册(T4)、§5 复用设置(T3/T4)、§6 测试(T2/T5)——逐项有任务。
- **类型一致**:`SEOInput`/`LLMsInput` 字段、`BuildSEOHead`/`BuildRobotsTxt`/`BuildSitemapXML`/`BuildLLMsTxt` 在测试(T2)、注入(T3)、handler(T4)中签名一致。
- **schema-drift**:`frontend_url` 只加到 InjectionPayload(注入用),不改 dto——测试逻辑允许 injection 多字段(T3 已说明);T3 Step4 显式回归该测试。
- **风险点**:`GetSiteSubtitle`/`GetDocURL` 可能不存在,T4 Step3 先 grep 确认再决定复用或补最小 getter(给了完整代码),避免 placeholder。
- **lint**:本仓库 CI 跑 golangci-lint v2.9;新增 Go 代码已按 gofmt 风格书写,T5 Step1 显式跑 lint 确保 0 issues(避免再次让 CI 变红)。
