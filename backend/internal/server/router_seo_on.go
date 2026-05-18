//go:build embed

package server

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/web"
	"github.com/gin-gonic/gin"
)

// registerSEOEndpoints wires /robots.txt /sitemap.xml /llms.txt onto the gin engine.
// embed-only: depends on the seo.Registry held by FrontendServer.
func registerSEOEndpoints(r *gin.Engine, fs *web.FrontendServer, svc *service.SettingService, siteURL string) {
	seoH := handler.NewSEOHandler(
		fs.SEORegistry(),
		siteURL,
		&legalDocSourceAdapter{svc: svc},
	)
	r.GET("/robots.txt", seoH.Robots)
	r.GET("/sitemap.xml", seoH.Sitemap)
	r.GET("/llms.txt", seoH.LLMsTxt)
}

// legalDocSourceAdapter bridges SettingService's public settings (which carry
// login_agreement_documents) into the LegalDocSource interface SEOHandler needs.
type legalDocSourceAdapter struct {
	svc *service.SettingService
}

func (a *legalDocSourceAdapter) ListPublic() []handler.LegalDoc {
	ctx, cancel := contextWithTimeout()
	defer cancel()
	s, err := a.svc.GetPublicSettings(ctx)
	if err != nil || s == nil {
		return nil
	}
	out := make([]handler.LegalDoc, 0, len(s.LoginAgreementDocuments))
	for _, d := range s.LoginAgreementDocuments {
		out = append(out, handler.LegalDoc{ID: d.ID, Title: d.Title})
	}
	return out
}
