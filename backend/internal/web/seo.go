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

func trimSlash(s string) string { return strings.TrimRight(strings.TrimSpace(s), "/") }

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
