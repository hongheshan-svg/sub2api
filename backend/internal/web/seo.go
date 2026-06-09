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

// sw writes s to b; strings.Builder.WriteString never returns a non-nil error,
// the discard satisfies errcheck without noise at every call site.
func sw(b *strings.Builder, s string) {
	_, _ = b.WriteString(s)
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
	sw(&b, "\n")
	if base != "" {
		sw(&b, `<link rel="canonical" href="`+e(base+"/")+`" />`+"\n")
		sw(&b, `<meta property="og:url" content="`+e(base+"/")+`" />`+"\n")
	}
	sw(&b, `<meta property="og:site_name" content="`+e(name)+`" />`+"\n")
	sw(&b, `<meta property="og:title" content="`+e(title)+`" />`+"\n")
	sw(&b, `<meta property="og:description" content="`+e(desc)+`" />`+"\n")
	sw(&b, `<meta property="og:image" content="`+e(absURL(base, logo))+`" />`+"\n")
	sw(&b, `<meta name="twitter:title" content="`+e(title)+`" />`+"\n")
	sw(&b, `<meta name="twitter:description" content="`+e(desc)+`" />`+"\n")

	siteURL := base + "/"
	if base == "" {
		siteURL = "/"
	}
	org := map[string]any{"@context": "https://schema.org", "@type": "Organization", "name": name, "url": siteURL, "logo": absURL(base, logo)}
	site := map[string]any{"@context": "https://schema.org", "@type": "WebSite", "name": name, "url": siteURL}
	app := map[string]any{"@context": "https://schema.org", "@type": "SoftwareApplication", "name": name, "applicationCategory": "DeveloperApplication", "operatingSystem": "Any", "description": desc}
	for _, ld := range []any{org, site, app} {
		sw(&b, `<script type="application/ld+json">`+jsonLD(ld)+`</script>`+"\n")
	}
	return []byte(b.String())
}

// BuildLandingHead 返回某个 SEO 落地页要插入 </head> 前的标签:
// per-page canonical / og / twitter + FAQPage + BreadcrumbList JSON-LD。
// 命中落地页时用它取代全站 BuildSEOHead,避免标签重复。
func BuildLandingHead(p LandingPage, baseURL string) []byte {
	base := trimSlash(baseURL)
	canonical := p.Path
	if base != "" {
		canonical = base + p.Path
	}
	home := "/"
	if base != "" {
		home = base + "/"
	}
	e := html.EscapeString
	var b strings.Builder
	sw(&b, "\n")
	sw(&b, `<link rel="canonical" href="`+e(canonical)+`" />`+"\n")
	sw(&b, `<meta property="og:type" content="article" />`+"\n")
	sw(&b, `<meta property="og:url" content="`+e(canonical)+`" />`+"\n")
	sw(&b, `<meta property="og:title" content="`+e(p.Title)+`" />`+"\n")
	sw(&b, `<meta property="og:description" content="`+e(p.Description)+`" />`+"\n")
	sw(&b, `<meta name="twitter:title" content="`+e(p.Title)+`" />`+"\n")
	sw(&b, `<meta name="twitter:description" content="`+e(p.Description)+`" />`+"\n")

	if len(p.FAQ) > 0 {
		mains := make([]map[string]any, 0, len(p.FAQ))
		for _, f := range p.FAQ {
			mains = append(mains, map[string]any{
				"@type": "Question", "name": f.Q,
				"acceptedAnswer": map[string]any{"@type": "Answer", "text": f.A},
			})
		}
		faq := map[string]any{"@context": "https://schema.org", "@type": "FAQPage", "mainEntity": mains}
		sw(&b, `<script type="application/ld+json">`+jsonLD(faq)+`</script>`+"\n")
	}

	crumb := map[string]any{
		"@context": "https://schema.org", "@type": "BreadcrumbList",
		"itemListElement": []map[string]any{
			{"@type": "ListItem", "position": 1, "name": "Home", "item": home},
			{"@type": "ListItem", "position": 2, "name": p.Kicker, "item": canonical},
		},
	}
	sw(&b, `<script type="application/ld+json">`+jsonLD(crumb)+`</script>`+"\n")
	return []byte(b.String())
}

// marketingRoute 是一条面向搜索引擎 / AI 引擎公开的营销或文档路由。
type marketingRoute struct {
	path, freq, prio, label string
}

// marketingRoutes 是公开的营销 / 文档路由清单,同时驱动 sitemap.xml 与 llms.txt,
// 保证两者始终一致。新增 SEO landing page 时只需在这里登记一次。
// 必须与 frontend/src/router/index.ts 中的路由保持同步。
var marketingRoutes = []marketingRoute{
	{"/", "daily", "1.0", "首页：产品介绍与定价"},
	{"/pricing", "weekly", "0.8", "定价"},
	{"/docs/quick-start", "weekly", "0.9", "快速接入指南"},
	{"/docs/troubleshooting", "weekly", "0.7", "常见问题与排障"},
	{"/claude-code-api-gateway", "weekly", "0.95", "Claude Code API Gateway"},
	{"/claude-code-base-url", "weekly", "0.9", "Claude Code base_url 配置"},
	{"/codex-api-gateway", "weekly", "0.9", "Codex API Gateway"},
	{"/gemini-cli-api-gateway", "weekly", "0.85", "Gemini CLI API Gateway"},
	{"/openai-compatible-api-gateway", "weekly", "0.9", "OpenAI 兼容 API Gateway"},
	{"/gpt-image-2-api", "weekly", "0.85", "GPT-Image-2 API"},
	{"/cc-switch-provider-config", "weekly", "0.8", "CC Switch 多 Provider/Key/模型配置"},
	{"/compare/claude-code-vs-codex", "weekly", "0.75", "Claude Code vs Codex 对比"},
}

// BuildRobotsTxt 生成 robots.txt 内容;放行主流 AI 爬虫。
func BuildRobotsTxt(baseURL string) string {
	base := trimSlash(baseURL)
	sitemap := "/sitemap.xml"
	if base != "" {
		sitemap = base + "/sitemap.xml"
	}
	// 覆盖 OpenAI / Anthropic / Perplexity / Google 的索引与实时检索爬虫。
	aiBots := []string{
		"GPTBot", "OAI-SearchBot", "ChatGPT-User",
		"ClaudeBot", "Claude-User",
		"PerplexityBot",
		"Google-Extended", "GoogleOther",
		"CCBot",
	}
	var b strings.Builder
	sw(&b, "User-agent: *\n")
	sw(&b, "Allow: /\n")
	for _, d := range []string{"/admin", "/dashboard", "/api/", "/v1/", "/backend-api/", "/auth/"} {
		sw(&b, "Disallow: "+d+"\n")
	}
	sw(&b, "\n# AI crawlers (GEO)\n")
	for _, ua := range aiBots {
		sw(&b, "User-agent: "+ua+"\nAllow: /\n")
	}
	sw(&b, "\nSitemap: "+sitemap+"\n")
	return b.String()
}

// BuildSitemapXML 生成 sitemap.xml,列出公开营销与文档路由(见 marketingRoutes)。
func BuildSitemapXML(baseURL string) string {
	base := trimSlash(baseURL)
	var b strings.Builder
	sw(&b, `<?xml version="1.0" encoding="UTF-8"?>`+"\n")
	sw(&b, `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`+"\n")
	for _, x := range marketingRoutes {
		loc := x.path
		if base != "" {
			loc = base + x.path
		}
		sw(&b, "  <url><loc>"+loc+"</loc><changefreq>"+x.freq+"</changefreq><priority>"+x.prio+"</priority></url>\n")
	}
	sw(&b, "</urlset>\n")
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
	abs := func(p string) string {
		if base == "" {
			return p
		}
		return base + p
	}
	var b strings.Builder
	sw(&b, "# "+name+"\n\n")
	sw(&b, "> "+subtitle+"\n\n")
	sw(&b, name+" 是一个面向 Claude Code、Codex、Gemini CLI、VS Code 等 AI 编程工具的 OpenAI 兼容 API 网关 / 中转平台,只需配置 base_url 和 API Key 即可统一接入 Claude、GPT、Gemini 等模型。\n\n")
	sw(&b, "## 主要页面\n")
	sw(&b, "- [首页]("+abs("/")+"): 产品介绍与定价\n")
	sw(&b, "- [登录]("+abs("/login")+"): 用户登录/注册\n")
	if doc := strings.TrimSpace(in.DocURL); doc != "" {
		sw(&b, "- [接入文档]("+doc+"): API 接入说明\n")
	}
	sw(&b, "\n## 核心场景\n")
	for _, x := range marketingRoutes {
		if x.path == "/" {
			continue
		}
		sw(&b, "- ["+x.label+"]("+abs(x.path)+")\n")
	}
	return b.String()
}
