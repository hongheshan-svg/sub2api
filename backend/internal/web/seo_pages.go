package web

import (
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
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
	FAQ         []LandingFAQ     `json:"faq"`
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

// RenderLandingBody 把落地页内容渲染成语义化 HTML,注入空的 #app,
// 让非 JS 爬虫也能读到正文。所有文本字段经 HTML 转义。
func RenderLandingBody(p LandingPage) []byte {
	e := html.EscapeString
	var b strings.Builder
	sw(&b, "<main>")
	if p.Kicker != "" {
		sw(&b, "<p>"+e(p.Kicker)+"</p>")
	}
	sw(&b, "<h1>"+e(p.H1)+"</h1>")
	if p.Lead != "" {
		sw(&b, "<p>"+e(p.Lead)+"</p>")
	}
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
	if len(p.FAQ) > 0 {
		sw(&b, "<h2>常见问题</h2>")
		for _, f := range p.FAQ {
			sw(&b, "<details open><summary>"+e(f.Q)+"</summary><p>"+e(f.A)+"</p></details>")
		}
	}
	sw(&b, "</main>")
	return []byte(b.String())
}
