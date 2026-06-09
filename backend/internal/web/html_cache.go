//go:build embed

package web

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// HTMLCache caches rendered index.html per request path (SEO landing routes get
// their own entry; all other SPA routes share the "" key).
type HTMLCache struct {
	mu           sync.RWMutex
	entries      map[string]CachedHTML
	baseHTMLHash string
}

// CachedHTML represents one cached, rendered page.
type CachedHTML struct {
	Content []byte
	ETag    string
}

func NewHTMLCache() *HTMLCache {
	return &HTMLCache{entries: make(map[string]CachedHTML)}
}

func (c *HTMLCache) SetBaseHTML(baseHTML []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	hash := sha256.Sum256(baseHTML)
	c.baseHTMLHash = hex.EncodeToString(hash[:8])
}

// Invalidate drops all cached pages (call when settings change).
func (c *HTMLCache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]CachedHTML)
}

// Get returns the cached page for key, or nil if absent.
func (c *HTMLCache) Get(key string) *CachedHTML {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.entries[key]
	if !ok {
		return nil
	}
	return &v
}

// Set stores the rendered page for key.
func (c *HTMLCache) Set(key string, html []byte, settingsJSON []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = CachedHTML{Content: html, ETag: c.generateETag(key, settingsJSON)}
}

func (c *HTMLCache) generateETag(key string, settingsJSON []byte) string {
	h := sha256.Sum256(append([]byte(key+"\x00"), settingsJSON...))
	return `"` + c.baseHTMLHash + "-" + hex.EncodeToString(h[:8]) + `"`
}
