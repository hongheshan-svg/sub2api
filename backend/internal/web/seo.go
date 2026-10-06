package web

import (
	"encoding/json"
	"html"
	"sort"
	"strconv"
	"strings"
)

// SEOInput 是构建注入到 <head> 的 SEO 片段所需的输入。
type SEOInput struct {
	SiteName     string
	SiteSubtitle string
	// Title 是首页 og:title / twitter:title,通常取 index.html 中已含站点名的 <title>;
	// 空时回退为 "<站点名> - AI API Gateway"。
	Title string
	// Description 是首页的完整描述(通常取自 index.html 的 meta description)。
	// 站点副标题往往只是一句口号("企业级中转"),直接拿来做 og:description 信息量太低;
	// 非空时优先用它,空时回退到副标题。
	Description string
	BaseURL     string // 站点绝对 URL,可能为空
	Logo        string // logo 路径或绝对 URL(data: URI 应先经 SiteLogoHref 转换)
	Lang        string // 如 "zh-CN"
}

// LLMsEndpoint 是 llms.txt 中列出的一个 API 接入地址(来自 custom_endpoints 设置)。
type LLMsEndpoint struct {
	Name        string `json:"name"`
	Endpoint    string `json:"endpoint"`
	Description string `json:"description"`
}

// ParseLLMsEndpoints 解析 custom_endpoints 设置的 JSON 数组;非法输入返回 nil。
func ParseLLMsEndpoints(raw string) []LLMsEndpoint {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var eps []LLMsEndpoint
	if err := json.Unmarshal([]byte(raw), &eps); err != nil {
		return nil
	}
	out := eps[:0]
	for _, ep := range eps {
		ep.Endpoint = strings.TrimSpace(ep.Endpoint)
		if strings.HasPrefix(ep.Endpoint, "https://") || strings.HasPrefix(ep.Endpoint, "http://") {
			out = append(out, ep)
		}
	}
	return out
}

// LLMsInput 是构建 /llms.txt 与 /llms-full.txt 所需的输入。
type LLMsInput struct {
	SiteName     string
	SiteSubtitle string
	BaseURL      string
	DocURL       string
	Endpoints    []LLMsEndpoint
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

// ogLocale converts a BCP-47 tag ("zh-CN") to the Open Graph form ("zh_CN").
func ogLocale(lang string) string {
	switch l := strings.TrimSpace(lang); {
	case l == "":
		return "zh_CN"
	case strings.EqualFold(l, "en"):
		return "en_US"
	default:
		return strings.ReplaceAll(l, "-", "_")
	}
}

func isEnglish(lang string) bool { return strings.HasPrefix(strings.ToLower(lang), "en") }

// BuildSEOHead 返回要插入 </head> 前的 SEO 标签片段。
func BuildSEOHead(in SEOInput) []byte {
	name := strings.TrimSpace(in.SiteName)
	if name == "" {
		name = "Sub2API"
	}
	desc := strings.TrimSpace(in.Description)
	if desc == "" {
		desc = strings.TrimSpace(in.SiteSubtitle)
	}
	if desc == "" {
		desc = "统一接入 Claude、GPT、Gemini 等大模型 API 的 AI 编码中转网关。"
	}
	base := trimSlash(in.BaseURL)
	logo := strings.TrimSpace(in.Logo)
	if logo == "" {
		logo = "/logo.png"
	}
	logoURL := absURL(base, logo)
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = name + " - AI API Gateway"
	}

	e := html.EscapeString
	var b strings.Builder
	sw(&b, "\n")
	if base != "" {
		sw(&b, `<link rel="canonical" href="`+e(base+"/")+`" />`+"\n")
		sw(&b, `<meta property="og:url" content="`+e(base+"/")+`" />`+"\n")
	}
	sw(&b, `<meta property="og:site_name" content="`+e(name)+`" />`+"\n")
	sw(&b, `<meta property="og:locale" content="`+e(ogLocale(in.Lang))+`" />`+"\n")
	sw(&b, `<meta property="og:title" content="`+e(title)+`" />`+"\n")
	sw(&b, `<meta property="og:description" content="`+e(desc)+`" />`+"\n")
	sw(&b, `<meta property="og:image" content="`+e(logoURL)+`" />`+"\n")
	sw(&b, `<meta name="twitter:title" content="`+e(title)+`" />`+"\n")
	sw(&b, `<meta name="twitter:description" content="`+e(desc)+`" />`+"\n")
	sw(&b, `<meta name="twitter:image" content="`+e(logoURL)+`" />`+"\n")

	org := map[string]any{"@context": "https://schema.org", "@type": "Organization", "name": name, "logo": logoURL}
	site := map[string]any{"@context": "https://schema.org", "@type": "WebSite", "name": name, "description": desc, "inLanguage": langOrDefault(in.Lang)}
	app := map[string]any{"@context": "https://schema.org", "@type": "SoftwareApplication", "name": name, "applicationCategory": "DeveloperApplication", "operatingSystem": "Any", "description": desc}
	// A relative "url" is invalid in schema.org; only emit it once the absolute
	// site URL is known.
	if base != "" {
		org["url"] = base + "/"
		site["url"] = base + "/"
		app["url"] = base + "/"
	}
	for _, ld := range []any{org, site, app} {
		sw(&b, `<script type="application/ld+json">`+jsonLD(ld)+`</script>`+"\n")
	}
	return []byte(b.String())
}

func langOrDefault(lang string) string {
	if l := strings.TrimSpace(lang); l != "" {
		return l
	}
	return "zh-CN"
}

// HreflangLink is one <link rel="alternate" hreflang="…" href="…"> entry.
type HreflangLink struct {
	Hreflang string
	Href     string
}

// counterpartPath returns the other-language path of a landing path following the
// /en/<slug> convention: "/x" <-> "/en/x".
func counterpartPath(path string) string {
	if strings.HasPrefix(path, "/en/") {
		return strings.TrimPrefix(path, "/en")
	}
	return "/en" + path
}

// langOf returns the page language, defaulting to "zh-CN" when unset.
func langOf(p LandingPage) string {
	return langOrDefault(p.Lang)
}

// HreflangLinksFor returns the alternate-language links for p — self, counterpart
// and x-default — or nil when p has no translated counterpart in byPath. Google
// only honours reciprocal pairs, so a page without a counterpart emits no
// hreflang. x-default points at the zh (root) page of the pair.
func HreflangLinksFor(p LandingPage, byPath map[string]LandingPage, baseURL string) []HreflangLink {
	cp := counterpartPath(p.Path)
	other, ok := byPath[cp]
	if !ok {
		return nil
	}
	base := trimSlash(baseURL)
	abs := func(pth string) string {
		if base == "" {
			return pth
		}
		return base + pth
	}
	zhPath := p.Path
	if strings.HasPrefix(p.Path, "/en/") {
		zhPath = cp
	}
	return []HreflangLink{
		{Hreflang: langOf(p), Href: abs(p.Path)},
		{Hreflang: langOf(other), Href: abs(other.Path)},
		{Hreflang: "x-default", Href: abs(zhPath)},
	}
}

// LandingSite carries the site-wide values a landing page head needs.
type LandingSite struct {
	Name    string // 站点名,空时回退 "Sub2API"
	BaseURL string // 站点绝对 URL,可能为空
	Logo    string // logo 路径或绝对 URL(data: URI 应先经 SiteLogoHref 转换)
}

// BuildLandingHead 返回某个 SEO 落地页要插入 </head> 前的标签:
// per-page canonical / hreflang / og / twitter + TechArticle + FAQPage + BreadcrumbList JSON-LD。
// 命中落地页时用它取代全站 BuildSEOHead,避免标签重复。alts 为空时不输出 hreflang。
func BuildLandingHead(p LandingPage, site LandingSite, alts []HreflangLink) []byte {
	base := trimSlash(site.BaseURL)
	name := strings.TrimSpace(site.Name)
	if name == "" {
		name = "Sub2API"
	}
	canonical := absURL(base, p.Path)
	home := absURL(base, "/")
	logo := strings.TrimSpace(site.Logo)
	if logo == "" {
		logo = "/logo.png"
	}
	logoURL := absURL(base, logo)
	lang := langOf(p)

	e := html.EscapeString
	var b strings.Builder
	sw(&b, "\n")
	sw(&b, `<link rel="canonical" href="`+e(canonical)+`" />`+"\n")
	for _, a := range alts {
		sw(&b, `<link rel="alternate" hreflang="`+e(a.Hreflang)+`" href="`+e(a.Href)+`" />`+"\n")
	}
	sw(&b, `<meta property="og:type" content="article" />`+"\n")
	sw(&b, `<meta property="og:site_name" content="`+e(name)+`" />`+"\n")
	sw(&b, `<meta property="og:locale" content="`+e(ogLocale(lang))+`" />`+"\n")
	sw(&b, `<meta property="og:url" content="`+e(canonical)+`" />`+"\n")
	sw(&b, `<meta property="og:title" content="`+e(p.Title)+`" />`+"\n")
	sw(&b, `<meta property="og:description" content="`+e(p.Description)+`" />`+"\n")
	sw(&b, `<meta property="og:image" content="`+e(logoURL)+`" />`+"\n")
	if u := strings.TrimSpace(p.Updated); u != "" {
		sw(&b, `<meta property="article:modified_time" content="`+e(u)+`" />`+"\n")
	}
	sw(&b, `<meta name="twitter:title" content="`+e(p.Title)+`" />`+"\n")
	sw(&b, `<meta name="twitter:description" content="`+e(p.Description)+`" />`+"\n")
	sw(&b, `<meta name="twitter:image" content="`+e(logoURL)+`" />`+"\n")

	org := map[string]any{"@type": "Organization", "name": name, "url": home, "logo": logoURL}
	article := map[string]any{
		"@context":         "https://schema.org",
		"@type":            "TechArticle",
		"headline":         firstNonEmpty(p.H1, p.Title),
		"description":      p.Description,
		"inLanguage":       lang,
		"url":              canonical,
		"mainEntityOfPage": canonical,
		"author":           org,
		"publisher":        org,
	}
	if u := strings.TrimSpace(p.Updated); u != "" {
		article["dateModified"] = u
	}
	sw(&b, `<script type="application/ld+json">`+jsonLD(article)+`</script>`+"\n")

	if len(p.FAQ) > 0 {
		mains := make([]map[string]any, 0, len(p.FAQ))
		for _, f := range p.FAQ {
			mains = append(mains, map[string]any{
				"@type": "Question", "name": f.Q,
				"acceptedAnswer": map[string]any{"@type": "Answer", "text": f.A},
			})
		}
		faq := map[string]any{"@context": "https://schema.org", "@type": "FAQPage", "inLanguage": lang, "mainEntity": mains}
		sw(&b, `<script type="application/ld+json">`+jsonLD(faq)+`</script>`+"\n")
	}

	crumb := map[string]any{
		"@context": "https://schema.org", "@type": "BreadcrumbList",
		"itemListElement": []map[string]any{
			{"@type": "ListItem", "position": 1, "name": landingLabels(lang).Home, "item": home},
			{"@type": "ListItem", "position": 2, "name": firstNonEmpty(p.Kicker, p.Title), "item": canonical},
		},
	}
	sw(&b, `<script type="application/ld+json">`+jsonLD(crumb)+`</script>`+"\n")
	return []byte(b.String())
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// robotsDisallow lists the private / API prefixes no crawler should fetch.
var robotsDisallow = []string{"/admin", "/dashboard", "/api/", "/v1/", "/v1beta/", "/backend-api/", "/antigravity/", "/auth/", "/setup/"}

// robotsAIBots lists AI crawlers we explicitly welcome (GEO): training crawlers
// (models learn the brand), search indexers (AI answer engines cite us) and
// user-triggered fetchers (live browsing in a chat).
var robotsAIBots = []string{
	// OpenAI
	"GPTBot", "OAI-SearchBot", "ChatGPT-User",
	// Anthropic
	"ClaudeBot", "Claude-SearchBot", "Claude-User",
	// Perplexity
	"PerplexityBot", "Perplexity-User",
	// Google (Gemini / AI Overviews opt-in tokens)
	"Google-Extended", "GoogleOther",
	// Apple Intelligence, Amazon, Meta, DuckDuckGo, Mistral
	"Applebot", "Applebot-Extended", "Amazonbot", "meta-externalagent", "DuckAssistBot", "MistralAI-User",
	// ByteDance (豆包) — the largest Chinese AI assistant's crawler
	"Bytespider",
	// Common Crawl (training data for most open models)
	"CCBot",
}

// BuildRobotsTxt 生成 robots.txt 内容;放行主流 AI 爬虫。
//
// A crawler obeys only the most specific group that names it, so the AI bots
// get the same Disallow rules as "*" in one shared group — a bare
// "User-agent: GPTBot / Allow: /" group would silently open /admin, /api/ …
// to exactly the bots it names.
func BuildRobotsTxt(baseURL string) string {
	base := trimSlash(baseURL)
	sitemap := "/sitemap.xml"
	if base != "" {
		sitemap = base + "/sitemap.xml"
	}
	rules := func(b *strings.Builder) {
		sw(b, "Allow: /\n")
		for _, d := range robotsDisallow {
			sw(b, "Disallow: "+d+"\n")
		}
	}
	var b strings.Builder
	sw(&b, "User-agent: *\n")
	rules(&b)
	sw(&b, "\n# AI crawlers (GEO): explicitly welcome, same rules as above\n")
	for _, ua := range robotsAIBots {
		sw(&b, "User-agent: "+ua+"\n")
	}
	rules(&b)
	sw(&b, "\nSitemap: "+sitemap+"\n")
	return b.String()
}

// sitemapEntries 在落地页前面加上固定的首页条目(内容源 JSON 不含 /)。
func sitemapEntries(landing []LandingPage) []LandingPage {
	out := make([]LandingPage, 0, len(landing)+1)
	out = append(out, LandingPage{Path: "/", ChangeFreq: "daily", Priority: "1.0", Updated: latestUpdated(landing)})
	out = append(out, landing...)
	return out
}

// latestUpdated returns the newest Updated date among pages ("" if none). The
// homepage links every guide, so it changes whenever one of them does.
func latestUpdated(pages []LandingPage) string {
	dates := make([]string, 0, len(pages))
	for _, p := range pages {
		if u := strings.TrimSpace(p.Updated); u != "" {
			dates = append(dates, u)
		}
	}
	if len(dates) == 0 {
		return ""
	}
	sort.Strings(dates) // ISO 8601 dates sort lexically
	return dates[len(dates)-1]
}

// BuildSitemapXML 生成 sitemap.xml,首页 + 全部落地页(见 landing-pages.json)。
func BuildSitemapXML(baseURL string, landing []LandingPage) string {
	base := trimSlash(baseURL)
	byPath := make(map[string]LandingPage, len(landing))
	for _, p := range landing {
		byPath[p.Path] = p
	}
	x := html.EscapeString
	var b strings.Builder
	sw(&b, `<?xml version="1.0" encoding="UTF-8"?>`+"\n")
	sw(&b, `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml">`+"\n")
	for _, p := range sitemapEntries(landing) {
		loc := p.Path
		if base != "" {
			loc = base + p.Path
		}
		freq := p.ChangeFreq
		if freq == "" {
			freq = "weekly"
		}
		prio := p.Priority
		if prio == "" {
			prio = "0.7"
		}
		sw(&b, "  <url><loc>"+x(loc)+"</loc>")
		if u := strings.TrimSpace(p.Updated); u != "" {
			sw(&b, "<lastmod>"+x(u)+"</lastmod>")
		}
		sw(&b, "<changefreq>"+x(freq)+"</changefreq><priority>"+x(prio)+"</priority>")
		// Bilingual pages annotate each <url> with xhtml:link alternates (self +
		// counterpart + x-default); single-language pages stay compact.
		for _, a := range HreflangLinksFor(p, byPath, baseURL) {
			sw(&b, `<xhtml:link rel="alternate" hreflang="`+x(a.Hreflang)+`" href="`+x(a.Href)+`"/>`)
		}
		sw(&b, "</url>\n")
	}
	sw(&b, "</urlset>\n")
	return b.String()
}

// llmsIntro is the one-paragraph product summary shared by llms.txt and
// llms-full.txt. It names the native API shapes so an LLM answering "how do I
// point Claude Code / Codex at X" gets the protocol right.
func llmsIntro(name string) string {
	return name + " 是面向 Claude Code、Codex CLI、Gemini CLI、VS Code 插件等 AI 编程工具的 AI API 网关。" +
		"它同时提供 Anthropic Messages(/v1/messages)、OpenAI Chat Completions / Responses(/v1/chat/completions、/v1/responses)" +
		"与 Gemini 原生(/v1beta)接口,工具侧只需把 base_url 指向网关并填入平台签发的 API Key 即可接入 Claude、GPT、Gemini 等模型。\n"
}

func llmsDefaults(in LLMsInput) (name, subtitle string, abs func(string) string) {
	name = strings.TrimSpace(in.SiteName)
	if name == "" {
		name = "Sub2API"
	}
	subtitle = strings.TrimSpace(in.SiteSubtitle)
	if subtitle == "" {
		subtitle = "AI API 网关 / 中转平台"
	}
	base := trimSlash(in.BaseURL)
	abs = func(p string) string {
		if base == "" {
			return p
		}
		return base + p
	}
	return name, subtitle, abs
}

func splitByLang(pages []LandingPage) (zh, en []LandingPage) {
	for _, p := range pages {
		if isEnglish(langOf(p)) {
			en = append(en, p)
		} else {
			zh = append(zh, p)
		}
	}
	return zh, en
}

// BuildLLMsTxt 生成 /llms.txt(GEO,见 llmstxt.org),核心场景由落地页派生。
func BuildLLMsTxt(in LLMsInput, landing []LandingPage) string {
	name, subtitle, abs := llmsDefaults(in)
	var b strings.Builder
	sw(&b, "# "+name+"\n\n")
	sw(&b, "> "+subtitle+"\n\n")
	sw(&b, llmsIntro(name)+"\n")
	if len(in.Endpoints) > 0 {
		sw(&b, "## 接入地址 / API endpoints\n")
		for _, ep := range in.Endpoints {
			line := "- " + firstNonEmpty(ep.Name, ep.Endpoint) + ": " + ep.Endpoint
			if d := strings.TrimSpace(ep.Description); d != "" {
				line += " — " + d
			}
			sw(&b, line+"\n")
		}
		sw(&b, "\n")
	}
	sw(&b, "## 主要页面\n")
	sw(&b, "- [首页]("+abs("/")+"): 产品介绍与定价\n")
	sw(&b, "- [登录]("+abs("/login")+"): 用户登录/注册\n")
	if doc := strings.TrimSpace(in.DocURL); doc != "" {
		sw(&b, "- [接入文档]("+doc+"): API 接入说明\n")
	}
	if len(landing) > 0 {
		sw(&b, "- [完整内容]("+abs("/llms-full.txt")+"): 全部指南的纯文本版本,含配置示例与 FAQ\n")
	}
	zh, en := splitByLang(landing)
	writeList := func(title string, pages []LandingPage) {
		if len(pages) == 0 {
			return
		}
		sw(&b, "\n## "+title+"\n")
		for _, p := range pages {
			line := "- [" + firstNonEmpty(p.Kicker, p.Title) + "](" + abs(p.Path) + ")"
			if d := strings.TrimSpace(p.Description); d != "" {
				line += ": " + d
			}
			sw(&b, line+"\n")
		}
	}
	writeList("核心场景", zh)
	writeList("English Guides", en)
	return b.String()
}

// BuildLLMsFullTxt 生成 /llms-full.txt:把全部落地页正文(含配置示例与 FAQ)
// 拼成一份 Markdown,供 AI 检索/问答一次性读取完整上下文。
func BuildLLMsFullTxt(in LLMsInput, landing []LandingPage) string {
	name, subtitle, abs := llmsDefaults(in)
	var b strings.Builder
	sw(&b, "# "+name+"\n\n")
	sw(&b, "> "+subtitle+"\n\n")
	sw(&b, llmsIntro(name))
	if len(in.Endpoints) > 0 {
		sw(&b, "\n接入地址 / API endpoints:\n")
		for _, ep := range in.Endpoints {
			line := "- " + firstNonEmpty(ep.Name, ep.Endpoint) + ": " + ep.Endpoint
			if d := strings.TrimSpace(ep.Description); d != "" {
				line += " — " + d
			}
			sw(&b, line+"\n")
		}
	}
	for _, p := range landing {
		l := landingLabels(langOf(p))
		sw(&b, "\n---\n\n")
		sw(&b, "## "+firstNonEmpty(p.H1, p.Title)+"\n\n")
		sw(&b, "URL: "+abs(p.Path)+"\n")
		if u := strings.TrimSpace(p.Updated); u != "" {
			sw(&b, l.Updated+": "+u+"\n")
		}
		if p.Lead != "" {
			sw(&b, "\n"+p.Lead+"\n")
		}
		for _, it := range p.Benefits {
			sw(&b, "\n### "+it.Title+"\n\n"+it.Text+"\n")
		}
		if len(p.Steps) > 0 {
			sw(&b, "\n### "+firstNonEmpty(p.GuideTitle, l.Steps)+"\n\n")
			for i, s := range p.Steps {
				sw(&b, strconv.Itoa(i+1)+". "+s+"\n")
			}
		}
		for _, sn := range p.Snippets {
			sw(&b, "\n### "+firstNonEmpty(sn.Title, l.Examples)+"\n\n")
			if sn.Note != "" {
				sw(&b, sn.Note+"\n\n")
			}
			sw(&b, "```"+sn.Lang+"\n"+strings.TrimRight(sn.Code, "\n")+"\n```\n")
		}
		if len(p.FAQ) > 0 {
			sw(&b, "\n### "+l.FAQ+"\n")
			for _, f := range p.FAQ {
				sw(&b, "\n**"+f.Q+"**\n\n"+f.A+"\n")
			}
		}
	}
	return b.String()
}
