//go:build embed

package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	htmlpkg "html"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

const (
	// NonceHTMLPlaceholder is the placeholder for nonce in HTML script tags
	NonceHTMLPlaceholder = "__CSP_NONCE_VALUE__"
)

const nonLandingCacheKey = "" // all non-landing SPA routes share one rendered cache entry

//go:embed all:dist
var frontendFS embed.FS

var (
	embeddedLandingOnce sync.Once
	embeddedLandingMap  map[string]LandingPage
	embeddedLandingList []LandingPage
)

func loadEmbeddedLanding() {
	embeddedLandingOnce.Do(func() {
		distFS, err := fs.Sub(frontendFS, "dist")
		if err != nil {
			return
		}
		m, list, err := LoadLandingPages(distFS)
		if err != nil {
			log.Printf("seo: no embedded landing pages: %v", err)
			return
		}
		embeddedLandingMap, embeddedLandingList = m, list
	})
}

// LandingPages returns the embedded SEO landing pages in JSON order (nil if absent).
func LandingPages() []LandingPage {
	loadEmbeddedLanding()
	return embeddedLandingList
}

// PublicSettingsProvider is an interface to fetch public settings
type PublicSettingsProvider interface {
	GetPublicSettingsForInjection(ctx context.Context) (any, error)
}

// FrontendServer serves the embedded frontend with settings injection
type FrontendServer struct {
	distFS      fs.FS
	fileServer  http.Handler
	baseHTML    []byte
	cache       *HTMLCache
	settings    PublicSettingsProvider
	overrideDir string // local file override directory
	landing     map[string]LandingPage
}

// NewFrontendServer creates a new frontend server with settings injection
func NewFrontendServer(settingsProvider PublicSettingsProvider) (*FrontendServer, error) {
	distFS, err := fs.Sub(frontendFS, "dist")
	if err != nil {
		return nil, err
	}

	// Read base HTML once
	file, err := distFS.Open("index.html")
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	baseHTML, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}

	cache := NewHTMLCache()
	cache.SetBaseHTML(baseHTML)

	loadEmbeddedLanding()

	return &FrontendServer{
		distFS:      distFS,
		fileServer:  http.FileServer(http.FS(distFS)),
		baseHTML:    baseHTML,
		cache:       cache,
		settings:    settingsProvider,
		overrideDir: filepath.Join("data", "public"),
		landing:     embeddedLandingMap,
	}, nil
}

// InvalidateCache invalidates the HTML cache (call when settings change)
func (s *FrontendServer) InvalidateCache() {
	if s != nil && s.cache != nil {
		s.cache.Invalidate()
	}
}

// Middleware returns the Gin middleware handler
func (s *FrontendServer) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path

		// Skip API routes
		if shouldBypassEmbeddedFrontend(path) {
			c.Next()
			return
		}

		cleanPath := strings.TrimPrefix(path, "/")
		if cleanPath == "" {
			cleanPath = "index.html"
		}

		// For index.html or SPA routes, serve with injected settings
		if cleanPath == "index.html" || !s.fileExists(cleanPath) {
			s.serveIndexHTMLForPath(c, path)
			return
		}

		// Try local override first
		if s.tryServeOverride(c, cleanPath) {
			return
		}

		// Serve static files normally
		s.fileServer.ServeHTTP(c.Writer, c.Request)
		c.Abort()
	}
}

func (s *FrontendServer) fileExists(path string) bool {
	file, err := s.distFS.Open(path)
	if err != nil {
		return false
	}
	_ = file.Close()
	return true
}

// tryServeOverride checks if a local override file exists and serves it.
// Files in overrideDir take precedence over embedded files.
func (s *FrontendServer) tryServeOverride(c *gin.Context, cleanPath string) bool {
	if s.overrideDir == "" {
		return false
	}
	filePath := filepath.Join(s.overrideDir, filepath.Clean("/"+cleanPath))
	info, err := os.Stat(filePath)
	if err != nil || info.IsDir() {
		return false
	}
	c.File(filePath)
	c.Abort()
	return true
}

func (s *FrontendServer) serveIndexHTMLForPath(c *gin.Context, routePath string) {
	nonce := middleware.GetNonceFromContext(c)

	cacheKey := nonLandingCacheKey
	if _, ok := s.landing[routePath]; ok {
		cacheKey = routePath
	}

	if cached := s.cache.Get(cacheKey); cached != nil {
		content := replaceNoncePlaceholder(cached.Content, nonce)
		c.Header("Cache-Control", "no-store, must-revalidate")
		c.Data(http.StatusOK, "text/html; charset=utf-8", content)
		c.Abort()
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	settings, err := s.settings.GetPublicSettingsForInjection(ctx)
	if err != nil {
		c.Data(http.StatusOK, "text/html; charset=utf-8", s.baseHTML)
		c.Abort()
		return
	}
	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		c.Data(http.StatusOK, "text/html; charset=utf-8", s.baseHTML)
		c.Abort()
		return
	}

	rendered := s.injectForPath(routePath, settingsJSON)
	s.cache.Set(cacheKey, rendered, settingsJSON)

	content := replaceNoncePlaceholder(rendered, nonce)
	c.Header("Cache-Control", "no-store, must-revalidate")
	c.Data(http.StatusOK, "text/html; charset=utf-8", content)
	c.Abort()
}

func (s *FrontendServer) injectForPath(routePath string, settingsJSON []byte) []byte {
	configScript := []byte(`<script nonce="` + NonceHTMLPlaceholder + `">window.__APP_CONFIG__=` + string(settingsJSON) + `;</script>`)

	var cfg struct {
		SiteName     string `json:"site_name"`
		SiteSubtitle string `json:"site_subtitle"`
		SiteLogo     string `json:"site_logo"`
		FrontendURL  string `json:"frontend_url"`
	}
	_ = json.Unmarshal(settingsJSON, &cfg)

	page, isLanding := s.landing[routePath]

	var headInject []byte
	if isLanding {
		headInject = BuildLandingHead(page, cfg.FrontendURL, HreflangLinksFor(page, s.landing, cfg.FrontendURL))
	} else {
		headInject = BuildSEOHead(SEOInput{
			SiteName:     cfg.SiteName,
			SiteSubtitle: cfg.SiteSubtitle,
			BaseURL:      cfg.FrontendURL,
			Logo:         cfg.SiteLogo,
			Lang:         "zh-CN",
		})
	}

	// Seed the SPA so it renders the landing content without a fetch/flash.
	if isLanding {
		if pageJSON, err := json.Marshal(page); err == nil {
			headInject = append(headInject, []byte(`<script nonce="`+NonceHTMLPlaceholder+`">window.__SEO_PAGE__=`+string(pageJSON)+`;</script>`)...)
		} else {
			log.Printf("seo: marshal __SEO_PAGE__ for %q: %v", routePath, err)
		}
	}

	headClose := []byte("</head>")
	inject := append(headInject, configScript...)
	base := s.baseHTML
	if isLanding {
		// Drop the homepage FAQPage baked into index.html; the landing page
		// injects its own page-specific FAQPage via BuildLandingHead, and two
		// FAQPage entities (one not matching visible content) hurt rich results.
		base = removeStaticHomeFAQ(base)
	}
	result := bytes.Replace(base, headClose, append(inject, headClose...), 1)

	if isLanding {
		result = setTitle(result, page.Title)
		result = setMetaDescription(result, page.Description)
		result = setHtmlLang(result, langOf(page))
		body := RenderLandingBody(page)
		appDiv := []byte(`<div id="app"></div>`)
		replacement := append([]byte(`<div id="app">`), body...)
		replacement = append(replacement, []byte(`</div>`)...)
		result = bytes.Replace(result, appDiv, replacement, 1)
	} else {
		result = injectSiteTitle(result, settingsJSON)
	}

	return result
}

// removeStaticHomeFAQ strips the homepage FAQPage JSON-LD block baked into
// index.html. Applied only to landing pages: each injects its own page-specific
// FAQPage via BuildLandingHead, and keeping the homepage block too would place two
// FAQPage entities — one not matching the page's visible content — on the URL.
// It removes the first <script type="application/ld+json"> block containing
// "FAQPage", so unrelated JSON-LD (Organization/WebSite/…) is left untouched.
func removeStaticHomeFAQ(htmlBytes []byte) []byte {
	open := []byte(`<script type="application/ld+json">`)
	closeTag := []byte(`</script>`)
	search := 0
	for {
		rel := bytes.Index(htmlBytes[search:], open)
		if rel == -1 {
			return htmlBytes
		}
		start := search + rel
		relClose := bytes.Index(htmlBytes[start:], closeTag)
		if relClose == -1 {
			return htmlBytes
		}
		end := start + relClose + len(closeTag)
		if bytes.Contains(htmlBytes[start:end], []byte(`"FAQPage"`)) {
			// Swallow trailing whitespace left by the removed block.
			for end < len(htmlBytes) {
				switch htmlBytes[end] {
				case '\n', '\r', ' ', '\t':
					end++
					continue
				}
				break
			}
			var buf bytes.Buffer
			buf.Write(htmlBytes[:start])
			buf.Write(htmlBytes[end:])
			return buf.Bytes()
		}
		search = end
	}
}

// setMetaDescription replaces the content value of the static
// <meta name="description" content="…"> tag with desc. No-op when desc is empty
// (keeps the site-wide fallback from index.html) or the tag is absent. Used to give
// each SEO landing page its own meta description instead of the homepage's.
func setMetaDescription(htmlBytes []byte, desc string) []byte {
	if strings.TrimSpace(desc) == "" {
		return htmlBytes
	}
	marker := []byte(`<meta name="description" content="`)
	start := bytes.Index(htmlBytes, marker)
	if start == -1 {
		return htmlBytes
	}
	valStart := start + len(marker)
	rel := bytes.IndexByte(htmlBytes[valStart:], '"')
	if rel == -1 {
		return htmlBytes
	}
	valEnd := valStart + rel
	var buf bytes.Buffer
	buf.Write(htmlBytes[:valStart])
	buf.WriteString(htmlpkg.EscapeString(desc))
	buf.Write(htmlBytes[valEnd:])
	return buf.Bytes()
}

// setHtmlLang sets the lang attribute on the root <html lang="…"> element. The
// base index.html ships lang="zh-CN"; English landing pages need lang="en" so the
// page language matches its content for crawlers and assistive tech.
func setHtmlLang(htmlBytes []byte, lang string) []byte {
	lang = strings.TrimSpace(lang)
	if lang == "" {
		return htmlBytes
	}
	marker := []byte(`<html lang="`)
	start := bytes.Index(htmlBytes, marker)
	if start == -1 {
		return htmlBytes
	}
	valStart := start + len(marker)
	rel := bytes.IndexByte(htmlBytes[valStart:], '"')
	if rel == -1 {
		return htmlBytes
	}
	valEnd := valStart + rel
	var buf bytes.Buffer
	buf.Write(htmlBytes[:valStart])
	buf.WriteString(htmlpkg.EscapeString(lang))
	buf.Write(htmlBytes[valEnd:])
	return buf.Bytes()
}

// setTitle replaces the <title>…</title> contents with title.
func setTitle(html []byte, title string) []byte {
	titleStart := bytes.Index(html, []byte("<title>"))
	titleEnd := bytes.Index(html, []byte("</title>"))
	if titleStart == -1 || titleEnd == -1 || titleEnd <= titleStart {
		return html
	}
	var buf bytes.Buffer
	buf.Write(html[:titleStart])
	buf.WriteString("<title>" + title + "</title>")
	buf.Write(html[titleEnd+len("</title>"):])
	return buf.Bytes()
}

// injectSiteTitle sets the tab title to "<site_name> - AI API Gateway".
func injectSiteTitle(html, settingsJSON []byte) []byte {
	var cfg struct {
		SiteName string `json:"site_name"`
	}
	if err := json.Unmarshal(settingsJSON, &cfg); err != nil || cfg.SiteName == "" {
		return html
	}
	return setTitle(html, htmlpkg.EscapeString(cfg.SiteName)+" - AI API Gateway")
}

// replaceNoncePlaceholder replaces the nonce placeholder with actual nonce value
func replaceNoncePlaceholder(html []byte, nonce string) []byte {
	return bytes.ReplaceAll(html, []byte(NonceHTMLPlaceholder), []byte(nonce))
}

// ServeEmbeddedFrontend returns a middleware for serving embedded frontend
// This is the legacy function for backward compatibility when no settings provider is available
func ServeEmbeddedFrontend() gin.HandlerFunc {
	distFS, err := fs.Sub(frontendFS, "dist")
	if err != nil {
		panic("failed to get dist subdirectory: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(distFS))
	overrideDir := filepath.Join("data", "public")

	return func(c *gin.Context) {
		path := c.Request.URL.Path

		if shouldBypassEmbeddedFrontend(path) {
			c.Next()
			return
		}

		cleanPath := strings.TrimPrefix(path, "/")
		if cleanPath == "" {
			cleanPath = "index.html"
		}

		if file, err := distFS.Open(cleanPath); err == nil {
			_ = file.Close()
			// Try local override first
			if tryServeOverrideFile(c, overrideDir, cleanPath) {
				return
			}
			fileServer.ServeHTTP(c.Writer, c.Request)
			c.Abort()
			return
		}

		serveIndexHTML(c, distFS)
	}
}

// tryServeOverrideFile is a standalone version of tryServeOverride for legacy usage.
func tryServeOverrideFile(c *gin.Context, overrideDir, cleanPath string) bool {
	if overrideDir == "" {
		return false
	}
	filePath := filepath.Join(overrideDir, filepath.Clean("/"+cleanPath))
	info, err := os.Stat(filePath)
	if err != nil || info.IsDir() {
		return false
	}
	c.File(filePath)
	c.Abort()
	return true
}

func shouldBypassEmbeddedFrontend(path string) bool {
	trimmed := strings.TrimSpace(path)
	return strings.HasPrefix(trimmed, "/api/") ||
		strings.HasPrefix(trimmed, "/v1/") ||
		strings.HasPrefix(trimmed, "/v1beta/") ||
		strings.HasPrefix(trimmed, "/backend-api/") ||
		strings.HasPrefix(trimmed, "/antigravity/") ||
		strings.HasPrefix(trimmed, "/setup/") ||
		trimmed == "/health" ||
		trimmed == "/responses" ||
		strings.HasPrefix(trimmed, "/responses/") ||
		strings.HasPrefix(trimmed, "/images/") ||
		trimmed == "/robots.txt" ||
		trimmed == "/sitemap.xml" ||
		trimmed == "/llms.txt"
}

func serveIndexHTML(c *gin.Context, fsys fs.FS) {
	file, err := fsys.Open("index.html")
	if err != nil {
		c.String(http.StatusNotFound, "Frontend not found")
		c.Abort()
		return
	}
	defer func() { _ = file.Close() }()

	content, err := io.ReadAll(file)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to read index.html")
		c.Abort()
		return
	}

	c.Data(http.StatusOK, "text/html; charset=utf-8", content)
	c.Abort()
}

func HasEmbeddedFrontend() bool {
	_, err := frontendFS.ReadFile("dist/index.html")
	return err == nil
}
