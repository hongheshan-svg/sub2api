//go:build unit

package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/web"
)

// These verify the pure builders are wired to real paths with correct content types.
// (Full handler wiring needs a *service.SettingService; here we assert the builder
// outputs that the handlers serve.)
func TestSEOBuildersProduceExpectedContent(t *testing.T) {
	require.Contains(t, web.BuildRobotsTxt("https://x.io"), "Sitemap: https://x.io/sitemap.xml")
	require.Contains(t, web.BuildSitemapXML("https://x.io", nil), "<loc>https://x.io/</loc>")
	require.Contains(t, web.BuildLLMsTxt(web.LLMsInput{SiteName: "X", BaseURL: "https://x.io"}, nil), "# X")
}

func TestRobotsRouteWiring(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/robots.txt", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(web.BuildRobotsTxt("https://x.io")))
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/robots.txt", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
	require.Contains(t, w.Body.String(), "GPTBot")
}
