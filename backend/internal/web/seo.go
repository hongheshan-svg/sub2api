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

// sitemapEntries 在落地页前面加上固定的首页条目(内容源 JSON 不含 /)。
func sitemapEntries(landing []LandingPage) []LandingPage {
	out := make([]LandingPage, 0, len(landing)+1)
	out = append(out, LandingPage{Path: "/", ChangeFreq: "daily", Priority: "1.0"})
	out = append(out, landing...)
	return out
}

// BuildSitemapXML 生成 sitemap.xml,首页 + 全部落地页(见 landing-pages.json)。
func BuildSitemapXML(baseURL string, landing []LandingPage) string {
	base := trimSlash(baseURL)
	var b strings.Builder
	sw(&b, `<?xml version="1.0" encoding="UTF-8"?>`+"\n")
	sw(&b, `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`+"\n")
	for _, x := range sitemapEntries(landing) {
		loc := x.Path
		if base != "" {
			loc = base + x.Path
		}
		freq := x.ChangeFreq
		if freq == "" {
			freq = "weekly"
		}
		prio := x.Priority
		if prio == "" {
			prio = "0.7"
		}
		sw(&b, "  <url><loc>"+loc+"</loc><changefreq>"+freq+"</changefreq><priority>"+prio+"</priority></url>\n")
	}
	sw(&b, "</urlset>\n")
	return b.String()
}

// BuildLLMsTxt 生成 /llms.txt(GEO),核心场景由落地页派生。
func BuildLLMsTxt(in LLMsInput, landing []LandingPage) string {
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
	if len(landing) > 0 {
		sw(&b, "\n## 核心场景\n")
		for _, p := range landing {
			label := p.Kicker
			if label == "" {
				label = p.Title
			}
			sw(&b, "- ["+label+"]("+abs(p.Path)+")\n")
		}
	}
	return b.String()
}
