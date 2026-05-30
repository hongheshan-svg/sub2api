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
