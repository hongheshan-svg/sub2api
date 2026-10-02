//go:build unit

package web

import (
	"strings"
	"testing"
	"testing/fstest"

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
	require.NotContains(t, head, "</script></script>")
}

func TestBuildSEOHead_NoBaseURL_OmitsAbsoluteURLs(t *testing.T) {
	head := string(BuildSEOHead(SEOInput{SiteName: "GW-LINK", Logo: "/logo.png", Lang: "zh-CN"}))
	require.NotContains(t, head, `rel="canonical"`)
	require.NotContains(t, head, `og:url`)
	require.Contains(t, head, `property="og:title"`)
	require.Contains(t, head, `"@type":"Organization"`)
}

func TestBuildSEOHead_EscapesSiteName(t *testing.T) {
	head := string(BuildSEOHead(SEOInput{SiteName: `A<b>"x`, BaseURL: "https://x.io", Lang: "zh-CN"}))
	require.NotContains(t, head, `A<b>"x`)
	require.Contains(t, head, "A&lt;b&gt;")
}

func TestBuildRobotsTxt(t *testing.T) {
	r := BuildRobotsTxt("https://gw.example.com")
	for _, ua := range []string{
		"GPTBot", "OAI-SearchBot", "ChatGPT-User",
		"ClaudeBot", "Claude-User", "PerplexityBot",
		"Google-Extended", "GoogleOther", "CCBot",
	} {
		require.Contains(t, r, "User-agent: "+ua)
	}
	require.Contains(t, r, "Disallow: /admin")
	require.Contains(t, r, "Disallow: /api/")
	require.Contains(t, r, "Disallow: /auth/")
	require.Contains(t, r, "Sitemap: https://gw.example.com/sitemap.xml")
}

func TestBuildRobotsTxt_NoBaseURL_RelativeSitemap(t *testing.T) {
	require.Contains(t, BuildRobotsTxt(""), "Sitemap: /sitemap.xml")
}

func sampleLanding() []LandingPage {
	mk := func(path, prio string) LandingPage {
		var p LandingPage
		p.Path, p.Priority, p.ChangeFreq, p.Kicker = path, prio, "weekly", path
		return p
	}
	return []LandingPage{
		mk("/claude-code-api-gateway", "0.95"),
		mk("/pricing", "0.8"),
	}
}

func TestBuildSitemapXML(t *testing.T) {
	x := BuildSitemapXML("https://gw.example.com", sampleLanding())
	require.True(t, strings.HasPrefix(x, `<?xml version="1.0" encoding="UTF-8"?>`))
	require.Contains(t, x, "<loc>https://gw.example.com/</loc>")
	require.Contains(t, x, "<loc>https://gw.example.com/claude-code-api-gateway</loc>")
	require.Contains(t, x, "<loc>https://gw.example.com/pricing</loc>")
	require.NotContains(t, x, "<loc>https://gw.example.com/home</loc>")
	require.Contains(t, x, "</urlset>")
}

func TestBuildSitemapXML_Empty_FallsBackToHome(t *testing.T) {
	x := BuildSitemapXML("https://gw.example.com", nil)
	require.Contains(t, x, "<loc>https://gw.example.com/</loc>")
}

func TestBuildLLMsTxt(t *testing.T) {
	out := BuildLLMsTxt(LLMsInput{SiteName: "GW-LINK", SiteSubtitle: "AI 网关", BaseURL: "https://gw.example.com", DocURL: "https://docs.example.com"}, sampleLanding())
	require.Contains(t, out, "# GW-LINK")
	require.Contains(t, out, "> AI 网关")
	require.Contains(t, out, "https://gw.example.com/")
	require.Contains(t, out, "https://docs.example.com")
	require.Contains(t, out, "## 核心场景")
	require.Contains(t, out, "https://gw.example.com/claude-code-api-gateway")
}

func TestBuildLLMsTxt_NoDocURL_OmitsDocLine(t *testing.T) {
	out := BuildLLMsTxt(LLMsInput{SiteName: "GW-LINK", BaseURL: "https://gw.example.com"}, nil)
	require.NotContains(t, out, "接入文档")
}

func TestLoadLandingPages(t *testing.T) {
	fsys := fstest.MapFS{
		"seo/landing-pages.json": {Data: []byte(`[
			{"path":"/a","priority":"0.9","changefreq":"weekly","title":"A","h1":"H A","faq":[{"q":"q?","a":"yes"}]},
			{"path":"/b","priority":"0.8","changefreq":"monthly","title":"B","h1":"H B"}
		]`)},
	}
	byPath, ordered, err := LoadLandingPages(fsys)
	require.NoError(t, err)
	require.Len(t, ordered, 2)
	require.Equal(t, "/a", ordered[0].Path) // order preserved
	require.Equal(t, "/b", ordered[1].Path)
	require.Equal(t, "H A", byPath["/a"].H1)
	require.Equal(t, "yes", byPath["/a"].FAQ[0].A)
}

func TestLoadLandingPages_InvalidJSON(t *testing.T) {
	fsys := fstest.MapFS{"seo/landing-pages.json": {Data: []byte(`not json`)}}
	_, _, err := LoadLandingPages(fsys)
	require.Error(t, err)
}

func TestLoadLandingPages_Missing(t *testing.T) {
	_, _, err := LoadLandingPages(fstest.MapFS{})
	require.Error(t, err)
}

func TestRenderLandingBody(t *testing.T) {
	p := LandingPage{
		Kicker:     "K",
		H1:         "My <H1>",
		Lead:       "lead text",
		GuideTitle: "Guide",
		Steps:      []string{"step one"},
		Benefits:   []LandingBenefit{{Title: "Bene", Text: "btext"}},
		FAQ:        []LandingFAQ{{Q: "Why?", A: "Because"}},
	}
	body := string(RenderLandingBody(p, LandingLinks{}))
	require.Contains(t, body, "<h1>My &lt;H1&gt;</h1>") // escaped
	require.Contains(t, body, "<li>step one</li>")
	require.Contains(t, body, "Bene")
	require.Contains(t, body, "<summary>Why?</summary>")
	require.Contains(t, body, "Because")
}

func TestBuildLandingHead(t *testing.T) {
	p := LandingPage{
		Path:        "/claude-code-api-gateway",
		Title:       "Claude Code API Gateway - gw-link",
		Description: "desc",
		Kicker:      "Claude Code API Gateway",
		FAQ:         []LandingFAQ{{Q: "Q1", A: "A1"}},
	}
	head := string(BuildLandingHead(p, LandingSite{Name: "gw-link", BaseURL: "https://gw-link.com/"}, nil))
	require.Contains(t, head, `<link rel="canonical" href="https://gw-link.com/claude-code-api-gateway"`)
	require.NotContains(t, head, `href="https://gw-link.com/"`) // not the bare homepage
	require.Contains(t, head, `property="og:url" content="https://gw-link.com/claude-code-api-gateway"`)
	require.Contains(t, head, `property="og:title" content="Claude Code API Gateway - gw-link"`)
	require.Contains(t, head, `"@type":"FAQPage"`)
	require.Contains(t, head, `"@type":"BreadcrumbList"`)
	// No counterpart passed -> no hreflang alternates.
	require.NotContains(t, head, `rel="alternate" hreflang`)
}

func TestCounterpartPath(t *testing.T) {
	require.Equal(t, "/en/pricing", counterpartPath("/pricing"))
	require.Equal(t, "/pricing", counterpartPath("/en/pricing"))
	require.Equal(t, "/en/docs/quick-start", counterpartPath("/docs/quick-start"))
	require.Equal(t, "/docs/quick-start", counterpartPath("/en/docs/quick-start"))
}

func TestHreflangLinksFor(t *testing.T) {
	byPath := map[string]LandingPage{
		"/pricing":    {Path: "/pricing", Lang: "zh-CN"},
		"/en/pricing": {Path: "/en/pricing", Lang: "en"},
		"/zh-only":    {Path: "/zh-only", Lang: "zh-CN"},
	}

	// zh page with an en counterpart: self + counterpart + x-default(zh).
	alts := HreflangLinksFor(byPath["/pricing"], byPath, "https://gw-link.com")
	require.Equal(t, []HreflangLink{
		{Hreflang: "zh-CN", Href: "https://gw-link.com/pricing"},
		{Hreflang: "en", Href: "https://gw-link.com/en/pricing"},
		{Hreflang: "x-default", Href: "https://gw-link.com/pricing"},
	}, alts)

	// en page: x-default still points at the zh (root) path.
	altsEn := HreflangLinksFor(byPath["/en/pricing"], byPath, "https://gw-link.com")
	require.Equal(t, "x-default", altsEn[2].Hreflang)
	require.Equal(t, "https://gw-link.com/pricing", altsEn[2].Href)

	// Page without a counterpart: no hreflang (Google needs reciprocal pairs).
	require.Nil(t, HreflangLinksFor(byPath["/zh-only"], byPath, "https://gw-link.com"))
}

func TestBuildLandingHead_WithHreflang(t *testing.T) {
	p := LandingPage{Path: "/pricing", Title: "Pricing", Lang: "zh-CN"}
	alts := []HreflangLink{
		{Hreflang: "zh-CN", Href: "https://gw-link.com/pricing"},
		{Hreflang: "en", Href: "https://gw-link.com/en/pricing"},
		{Hreflang: "x-default", Href: "https://gw-link.com/pricing"},
	}
	head := string(BuildLandingHead(p, LandingSite{Name: "gw-link", BaseURL: "https://gw-link.com"}, alts))
	require.Contains(t, head, `<link rel="alternate" hreflang="en" href="https://gw-link.com/en/pricing" />`)
	require.Contains(t, head, `<link rel="alternate" hreflang="x-default" href="https://gw-link.com/pricing" />`)
}

func TestBuildSitemapXML_Hreflang(t *testing.T) {
	landing := []LandingPage{
		{Path: "/pricing", Lang: "zh-CN"},
		{Path: "/en/pricing", Lang: "en"},
	}
	xml := BuildSitemapXML("https://gw-link.com", landing)
	require.Contains(t, xml, `xmlns:xhtml="http://www.w3.org/1999/xhtml"`)
	require.Contains(t, xml, `<xhtml:link rel="alternate" hreflang="en" href="https://gw-link.com/en/pricing"/>`)
	require.Contains(t, xml, `<loc>https://gw-link.com/en/pricing</loc>`)
}

func TestBuildSEOHead_PrefersDescriptionOverSubtitle(t *testing.T) {
	head := string(BuildSEOHead(SEOInput{
		SiteName: "gw-link", SiteSubtitle: "企业级中转", Description: "完整的首页描述",
		BaseURL: "https://gw-link.com", Logo: "/site-logo?v=abc", Lang: "zh-CN",
	}))
	require.Contains(t, head, `property="og:description" content="完整的首页描述"`)
	require.NotContains(t, head, "企业级中转")
	require.Contains(t, head, `property="og:image" content="https://gw-link.com/site-logo?v=abc"`)
	require.Contains(t, head, `name="twitter:image" content="https://gw-link.com/site-logo?v=abc"`)
	require.Contains(t, head, `property="og:locale" content="zh_CN"`)
	require.Contains(t, head, `"url":"https://gw-link.com/"`)
}

func TestBuildSEOHead_CustomTitle(t *testing.T) {
	head := string(BuildSEOHead(SEOInput{SiteName: "gw-link", Title: "gw-link - Claude Code 中转网关"}))
	require.Contains(t, head, `property="og:title" content="gw-link - Claude Code 中转网关"`)
	require.Contains(t, head, `name="twitter:title" content="gw-link - Claude Code 中转网关"`)
}

func TestBuildSEOHead_NoBaseURL_NoRelativeSchemaURL(t *testing.T) {
	head := string(BuildSEOHead(SEOInput{SiteName: "gw-link", Lang: "zh-CN"}))
	require.NotContains(t, head, `"url":"/"`)
}

// robotsGroups splits robots.txt into groups: each group is the set of
// user-agents followed by its rule lines.
func robotsGroups(txt string) []struct{ agents, rules []string } {
	var groups []struct{ agents, rules []string }
	inRules := false
	for _, line := range strings.Split(txt, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "Sitemap:") {
			continue
		}
		if ua, ok := strings.CutPrefix(line, "User-agent: "); ok {
			if inRules || len(groups) == 0 {
				groups = append(groups, struct{ agents, rules []string }{})
				inRules = false
			}
			groups[len(groups)-1].agents = append(groups[len(groups)-1].agents, ua)
			continue
		}
		inRules = true
		groups[len(groups)-1].rules = append(groups[len(groups)-1].rules, line)
	}
	return groups
}

func TestBuildRobotsTxt_AIBotsKeepDisallowRules(t *testing.T) {
	groups := robotsGroups(BuildRobotsTxt("https://gw.example.com"))
	require.NotEmpty(t, groups)
	for _, g := range groups {
		// Every group — including the named AI crawlers, which ignore "*" —
		// must carry the private-path Disallow rules.
		require.Contains(t, g.rules, "Disallow: /admin", "group %v", g.agents)
		require.Contains(t, g.rules, "Disallow: /api/", "group %v", g.agents)
	}
	var all []string
	for _, g := range groups {
		all = append(all, g.agents...)
	}
	for _, ua := range []string{"Claude-SearchBot", "Perplexity-User", "Bytespider", "Applebot-Extended"} {
		require.Contains(t, all, ua)
	}
}

func TestBuildSitemapXML_Lastmod(t *testing.T) {
	landing := []LandingPage{
		{Path: "/a", Updated: "2026-09-01"},
		{Path: "/b", Updated: "2026-10-02"},
		{Path: "/c"},
	}
	x := BuildSitemapXML("https://gw.example.com", landing)
	require.Contains(t, x, "<loc>https://gw.example.com/a</loc><lastmod>2026-09-01</lastmod>")
	// The homepage takes the newest guide date.
	require.Contains(t, x, "<loc>https://gw.example.com/</loc><lastmod>2026-10-02</lastmod>")
	require.Contains(t, x, "<loc>https://gw.example.com/c</loc><changefreq>")
}

func TestBuildLLMsTxt_EndpointsDescriptionsAndLanguages(t *testing.T) {
	landing := []LandingPage{
		{Path: "/claude-code-api-gateway", Kicker: "Claude Code API Gateway", Description: "中文描述", Lang: "zh-CN"},
		{Path: "/en/claude-code-api-gateway", Kicker: "Claude Code API Gateway", Description: "English description", Lang: "en"},
	}
	out := BuildLLMsTxt(LLMsInput{
		SiteName: "gw-link", BaseURL: "https://gw-link.com",
		Endpoints: ParseLLMsEndpoints(`[{"name":"主节点","endpoint":"https://gw-link.com","description":"三网优化"},{"name":"bad","endpoint":"javascript:alert(1)"}]`),
	}, landing)
	require.Contains(t, out, "- 主节点: https://gw-link.com — 三网优化")
	require.NotContains(t, out, "javascript:")
	require.Contains(t, out, "- [Claude Code API Gateway](https://gw-link.com/claude-code-api-gateway): 中文描述")
	require.Contains(t, out, "## English Guides\n- [Claude Code API Gateway](https://gw-link.com/en/claude-code-api-gateway): English description")
	require.Contains(t, out, "https://gw-link.com/llms-full.txt")
	require.Contains(t, out, "/v1/messages")
}

func TestBuildLLMsFullTxt(t *testing.T) {
	landing := []LandingPage{{
		Path: "/en/x", Lang: "en", H1: "Set up X", Lead: "lead", Updated: "2026-10-02",
		Steps:    []string{"one", "two"},
		Snippets: []LandingSnippet{{Title: "env", Lang: "bash", Code: "export A=1\n"}},
		FAQ:      []LandingFAQ{{Q: "Why?", A: "Because."}},
	}}
	out := BuildLLMsFullTxt(LLMsInput{SiteName: "gw-link", BaseURL: "https://gw-link.com"}, landing)
	require.Contains(t, out, "## Set up X")
	require.Contains(t, out, "URL: https://gw-link.com/en/x")
	require.Contains(t, out, "Last updated: 2026-10-02")
	require.Contains(t, out, "1. one\n2. two")
	require.Contains(t, out, "```bash\nexport A=1\n```")
	require.Contains(t, out, "### FAQ")
	require.Contains(t, out, "**Why?**")
}

func TestBuildLandingHead_ArticleAndImage(t *testing.T) {
	p := LandingPage{Path: "/en/pricing", Title: "Pricing", H1: "Pricing H1", Kicker: "Pricing", Lang: "en", Updated: "2026-10-02"}
	head := string(BuildLandingHead(p, LandingSite{Name: "gw-link", BaseURL: "https://gw-link.com", Logo: "/site-logo?v=1"}, nil))
	require.Contains(t, head, `"@type":"TechArticle"`)
	require.Contains(t, head, `"headline":"Pricing H1"`)
	require.Contains(t, head, `"dateModified":"2026-10-02"`)
	require.Contains(t, head, `property="article:modified_time" content="2026-10-02"`)
	require.Contains(t, head, `property="og:image" content="https://gw-link.com/site-logo?v=1"`)
	require.Contains(t, head, `property="og:site_name" content="gw-link"`)
	require.Contains(t, head, `property="og:locale" content="en_US"`)
	require.Contains(t, head, `"name":"Home"`)

	zh := string(BuildLandingHead(LandingPage{Path: "/pricing", Kicker: "Pricing"}, LandingSite{BaseURL: "https://gw-link.com"}, nil))
	require.Contains(t, zh, `"name":"首页"`)
	require.NotContains(t, zh, "dateModified")
}

func TestLandingLinksFor(t *testing.T) {
	ordered := []LandingPage{
		{Path: "/a", Lang: "zh-CN"}, {Path: "/b", Lang: "zh-CN"},
		{Path: "/en/a", Lang: "en"}, {Path: "/en/b", Lang: "en"},
	}
	byPath := map[string]LandingPage{}
	for _, p := range ordered {
		byPath[p.Path] = p
	}
	links := LandingLinksFor(ordered[0], ordered, byPath)
	require.Len(t, links.Related, 1)
	require.Equal(t, "/b", links.Related[0].Path)
	require.NotNil(t, links.Counterpart)
	require.Equal(t, "/en/a", links.Counterpart.Path)

	enLinks := LandingLinksFor(ordered[3], ordered, byPath)
	require.Equal(t, "/en/a", enLinks.Related[0].Path)
	require.Equal(t, "/b", enLinks.Counterpart.Path)
}

func TestRenderLandingBody_SnippetsLinksAndLabels(t *testing.T) {
	p := LandingPage{
		Path: "/en/a", Lang: "en", H1: "H", Updated: "2026-10-02",
		Snippets: []LandingSnippet{{Title: "Shell", Lang: "bash", Code: `export K="<v>"`, Note: "note"}},
		FAQ:      []LandingFAQ{{Q: "Q", A: "A"}},
	}
	cp := LandingPage{Path: "/a", Lang: "zh-CN"}
	body := string(RenderLandingBody(p, LandingLinks{
		Related:     []LandingPage{{Path: "/en/b", Kicker: "B guide"}},
		Counterpart: &cp,
	}))
	require.Contains(t, body, `<pre><code class="language-bash">export K=&#34;&lt;v&gt;&#34;</code></pre>`)
	require.Contains(t, body, "<h2>FAQ</h2>")
	require.NotContains(t, body, "常见问题")
	require.Contains(t, body, `<a href="/en/b">B guide</a>`)
	require.Contains(t, body, `<a href="/a" hreflang="zh-CN" lang="zh-CN">中文版</a>`)
	require.Contains(t, body, `<time datetime="2026-10-02">2026-10-02</time>`)
	require.Contains(t, body, `<a href="/login">Get started</a>`)
}

func TestRenderGuideLinks(t *testing.T) {
	require.Nil(t, RenderGuideLinks(nil))
	out := string(RenderGuideLinks([]LandingPage{
		{Path: "/a", Kicker: "A", Description: "desc a", Lang: "zh-CN"},
		{Path: "/en/a", Kicker: "A en", Lang: "en"},
	}))
	require.Contains(t, out, `<li><a href="/a">A</a> — desc a</li>`)
	require.Contains(t, out, `<h2>English guides</h2><ul><li><a href="/en/a">A en</a></li>`)
}

func TestSiteLogoHref(t *testing.T) {
	require.Equal(t, "/logo.png", SiteLogoHref("/logo.png"))
	require.Equal(t, "https://cdn.example.com/l.png", SiteLogoHref(" https://cdn.example.com/l.png "))
	href := SiteLogoHref("data:image/png;base64,AAAA")
	require.True(t, strings.HasPrefix(href, SiteLogoPath+"?v="), href)
	require.Equal(t, SiteLogoPath+"?v="+SiteLogoVersion("data:image/png;base64,AAAA"), href)
	require.NotEqual(t, href, SiteLogoHref("data:image/png;base64,BBBB"))
}

func TestDecodeDataImage(t *testing.T) {
	ct, data, ok := DecodeDataImage("data:image/png;base64,aGVsbG8=")
	require.True(t, ok)
	require.Equal(t, "image/png", ct)
	require.Equal(t, "hello", string(data))

	ct, data, ok = DecodeDataImage("data:image/svg+xml,%3Csvg%2F%3E")
	require.True(t, ok)
	require.Equal(t, "image/svg+xml", ct)
	require.Equal(t, "<svg/>", string(data))

	for _, bad := range []string{"", "/logo.png", "data:text/html;base64,PGI+", "data:image/png;base64,!!!", "data:image/png"} {
		_, _, ok := DecodeDataImage(bad)
		require.False(t, ok, bad)
	}
}
