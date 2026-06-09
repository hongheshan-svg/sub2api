package web

import (
	"encoding/json"
	"fmt"
	"io/fs"
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
