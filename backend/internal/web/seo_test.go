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
	body := string(RenderLandingBody(p))
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
	head := string(BuildLandingHead(p, "https://gw-link.com/", nil))
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
	head := string(BuildLandingHead(p, "https://gw-link.com", alts))
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
