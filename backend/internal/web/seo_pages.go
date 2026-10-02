package web

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"net/url"
	"strings"
)

// LandingBenefit 是落地页的一个价值点。
type LandingBenefit struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

// LandingFAQ 是落地页的一个常见问题。
type LandingFAQ struct {
	Q string `json:"q"`
	A string `json:"a"`
}

// LandingSnippet 是落地页上的一段可复制的配置/代码示例。具体、可直接复制的
// 配置是 AI 检索引用与搜索精选摘要最常抓取的内容,也是"薄内容"页面最缺的部分。
type LandingSnippet struct {
	Title string `json:"title"`
	Lang  string `json:"lang"` // 代码语言,如 bash / json / toml / python
	Code  string `json:"code"`
	Note  string `json:"note,omitempty"`
}

// LandingPage 是单个 SEO 落地页的内容与 sitemap 元数据,来源于
// frontend/public/seo/landing-pages.json(构建后位于 dist/seo/landing-pages.json)。
type LandingPage struct {
	Path        string           `json:"path"`
	ChangeFreq  string           `json:"changefreq"`
	Priority    string           `json:"priority"`
	Title       string           `json:"title"`
	Description string           `json:"description"`
	Kicker      string           `json:"kicker"`
	H1          string           `json:"h1"`
	Lead        string           `json:"lead"`
	GuideTitle  string           `json:"guideTitle"`
	Benefits    []LandingBenefit `json:"benefits"`
	Steps       []string         `json:"steps"`
	Snippets    []LandingSnippet `json:"snippets,omitempty"`
	FAQ         []LandingFAQ     `json:"faq"`
	// Updated is the ISO 8601 date (YYYY-MM-DD) the content last changed. It
	// feeds sitemap <lastmod>, article:modified_time and TechArticle
	// dateModified — freshness signals both search engines and AI retrieval use.
	Updated string `json:"updated,omitempty"`
	// Lang is the BCP-47 language of the page, e.g. "zh-CN" or "en". Empty is
	// treated as the default "zh-CN" for backward compatibility. The English
	// counterpart of a page lives at "/en"+path (see counterpartPath).
	Lang string `json:"lang,omitempty"`
}

// LoadLandingPages 从 fsys 读取 seo/landing-pages.json,返回按 path 索引的 map
// 与按 JSON 原序排列的切片。
func LoadLandingPages(fsys fs.FS) (map[string]LandingPage, []LandingPage, error) {
	data, err := fs.ReadFile(fsys, "seo/landing-pages.json")
	if err != nil {
		return nil, nil, err
	}
	var pages []LandingPage
	if err := json.Unmarshal(data, &pages); err != nil {
		return nil, nil, err
	}
	byPath := make(map[string]LandingPage, len(pages))
	for _, p := range pages {
		if _, dup := byPath[p.Path]; dup {
			return nil, nil, fmt.Errorf("seo/landing-pages.json: duplicate path %q", p.Path)
		}
		byPath[p.Path] = p
	}
	return byPath, pages, nil
}

// labels holds the fixed UI strings of a server-rendered landing page. They
// must match SeoLandingView.vue so the pre-hydration HTML and the SPA agree.
type labels struct {
	Home, FAQ, Related, Examples, Steps, Updated, GetStarted, Pricing, SwitchLang, Guides string
}

func landingLabels(lang string) labels {
	if isEnglish(lang) {
		return labels{
			Home: "Home", FAQ: "FAQ", Related: "Related guides", Examples: "Configuration examples",
			Steps: "Steps", Updated: "Last updated", GetStarted: "Get started", Pricing: "View pricing",
			SwitchLang: "中文版", Guides: "Guides",
		}
	}
	return labels{
		Home: "首页", FAQ: "常见问题", Related: "相关指南", Examples: "配置示例",
		Steps: "步骤", Updated: "最后更新", GetStarted: "立即体验", Pricing: "查看定价",
		SwitchLang: "English", Guides: "使用指南",
	}
}

// LandingLinks are the internal links rendered around a landing page body.
type LandingLinks struct {
	Related     []LandingPage // 同语言的其它落地页(按 JSON 顺序)
	Counterpart *LandingPage  // 另一语言的对应页,可为 nil
}

// LandingLinksFor returns the related same-language pages and the translated
// counterpart of p. Every landing page linking to its siblings turns the set
// into a crawlable cluster instead of sitemap-only orphans.
func LandingLinksFor(p LandingPage, ordered []LandingPage, byPath map[string]LandingPage) LandingLinks {
	var links LandingLinks
	lang := isEnglish(langOf(p))
	for _, o := range ordered {
		if o.Path != p.Path && isEnglish(langOf(o)) == lang {
			links.Related = append(links.Related, o)
		}
	}
	if cp, ok := byPath[counterpartPath(p.Path)]; ok {
		links.Counterpart = &cp
	}
	return links
}

// RenderLandingBody 把落地页内容渲染成语义化 HTML,注入空的 #app,
// 让非 JS 爬虫(多数 AI 爬虫、百度)也能读到正文与站内链接。所有文本字段经 HTML 转义。
func RenderLandingBody(p LandingPage, links LandingLinks) []byte {
	e := html.EscapeString
	l := landingLabels(langOf(p))
	var b strings.Builder
	sw(&b, "<main>")
	if p.Kicker != "" {
		sw(&b, "<p>"+e(p.Kicker)+"</p>")
	}
	sw(&b, "<h1>"+e(p.H1)+"</h1>")
	if p.Lead != "" {
		sw(&b, "<p>"+e(p.Lead)+"</p>")
	}
	sw(&b, `<p><a href="/login">`+e(l.GetStarted)+`</a> · <a href="/home#pricing">`+e(l.Pricing)+`</a></p>`)
	for _, it := range p.Benefits {
		sw(&b, "<section><h2>"+e(it.Title)+"</h2><p>"+e(it.Text)+"</p></section>")
	}
	if p.GuideTitle != "" {
		sw(&b, "<h2>"+e(p.GuideTitle)+"</h2>")
	}
	if len(p.Steps) > 0 {
		sw(&b, "<ol>")
		for _, s := range p.Steps {
			sw(&b, "<li>"+e(s)+"</li>")
		}
		sw(&b, "</ol>")
	}
	if len(p.Snippets) > 0 {
		sw(&b, "<section><h2>"+e(l.Examples)+"</h2>")
		for _, sn := range p.Snippets {
			if sn.Title != "" {
				sw(&b, "<h3>"+e(sn.Title)+"</h3>")
			}
			if sn.Note != "" {
				sw(&b, "<p>"+e(sn.Note)+"</p>")
			}
			cls := ""
			if sn.Lang != "" {
				cls = ` class="language-` + e(sn.Lang) + `"`
			}
			sw(&b, "<pre><code"+cls+">"+e(sn.Code)+"</code></pre>")
		}
		sw(&b, "</section>")
	}
	if len(p.FAQ) > 0 {
		sw(&b, "<h2>"+e(l.FAQ)+"</h2>")
		for _, f := range p.FAQ {
			sw(&b, "<details open><summary>"+e(f.Q)+"</summary><p>"+e(f.A)+"</p></details>")
		}
	}
	if len(links.Related) > 0 {
		sw(&b, `<nav aria-label="`+e(l.Related)+`"><h2>`+e(l.Related)+"</h2><ul>")
		for _, r := range links.Related {
			sw(&b, `<li><a href="`+e(r.Path)+`">`+e(firstNonEmpty(r.Kicker, r.Title))+"</a></li>")
		}
		sw(&b, "</ul></nav>")
	}
	if cp := links.Counterpart; cp != nil {
		sw(&b, `<p><a href="`+e(cp.Path)+`" hreflang="`+e(langOf(*cp))+`" lang="`+e(langOf(*cp))+`">`+e(l.SwitchLang)+"</a></p>")
	}
	if u := strings.TrimSpace(p.Updated); u != "" {
		sw(&b, `<p>`+e(l.Updated)+`: <time datetime="`+e(u)+`">`+e(u)+"</time></p>")
	}
	sw(&b, "</main>")
	return []byte(b.String())
}

// RenderGuideLinks renders the landing pages as a plain link list, grouped by
// language. It is injected into the homepage <noscript> so non-JS crawlers see
// the guides linked from the most authoritative URL on the site.
func RenderGuideLinks(landing []LandingPage) []byte {
	zh, en := splitByLang(landing)
	if len(zh)+len(en) == 0 {
		return nil
	}
	e := html.EscapeString
	var b strings.Builder
	group := func(title string, pages []LandingPage) {
		if len(pages) == 0 {
			return
		}
		sw(&b, "<h2>"+e(title)+"</h2><ul>")
		for _, p := range pages {
			sw(&b, `<li><a href="`+e(p.Path)+`">`+e(firstNonEmpty(p.Kicker, p.Title))+"</a>")
			if d := strings.TrimSpace(p.Description); d != "" {
				sw(&b, " — "+e(d))
			}
			sw(&b, "</li>")
		}
		sw(&b, "</ul>")
	}
	sw(&b, `<nav aria-label="Guides">`)
	group(landingLabels("zh-CN").Guides, zh)
	group("English guides", en)
	sw(&b, "</nav>")
	return []byte(b.String())
}

// SiteLogoPath serves an inline (data: URI) site logo as a real image file.
const SiteLogoPath = "/site-logo"

// isDataImageURI reports whether s is a data:image/… URI.
func isDataImageURI(s string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "data:image/")
}

// SiteLogoVersion is a short content hash of the logo setting, used as the
// cache-busting ?v= parameter of SiteLogoPath.
func SiteLogoVersion(logo string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(logo)))
	return hex.EncodeToString(sum[:6])
}

// SiteLogoHref returns the URL the HTML should reference the configured logo
// by. Admin-uploaded logos are stored as base64 data: URIs (hundreds of KB);
// inlining one into the favicon, og:image, JSON-LD and __APP_CONFIG__ bloats
// every page by several times its size and leaves og:image / Organization.logo
// unusable for crawlers. Such logos are swapped for SiteLogoPath with a content
// version; URLs and paths are returned unchanged.
func SiteLogoHref(logo string) string {
	logo = strings.TrimSpace(logo)
	if !isDataImageURI(logo) {
		return logo
	}
	return SiteLogoPath + "?v=" + SiteLogoVersion(logo)
}

// DecodeDataImage decodes a data:image/<type>[;base64],<payload> URI. ok is
// false for anything that is not a well-formed raster/SVG image URI.
func DecodeDataImage(uri string) (contentType string, data []byte, ok bool) {
	uri = strings.TrimSpace(uri)
	if !isDataImageURI(uri) {
		return "", nil, false
	}
	meta, payload, found := strings.Cut(uri[len("data:"):], ",")
	if !found {
		return "", nil, false
	}
	parts := strings.Split(meta, ";")
	contentType = strings.ToLower(strings.TrimSpace(parts[0]))
	switch contentType {
	case "image/png", "image/jpeg", "image/jpg", "image/gif", "image/webp", "image/svg+xml", "image/x-icon", "image/vnd.microsoft.icon", "image/avif":
	default:
		return "", nil, false
	}
	isBase64 := false
	for _, p := range parts[1:] {
		if strings.EqualFold(strings.TrimSpace(p), "base64") {
			isBase64 = true
		}
	}
	if isBase64 {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload))
		if err != nil {
			return "", nil, false
		}
		return contentType, decoded, true
	}
	unescaped, err := url.PathUnescape(payload)
	if err != nil {
		return "", nil, false
	}
	return contentType, []byte(unescaped), true
}
