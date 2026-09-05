package admin

import (
	"errors"
	"html"
	"net/http"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// kiroDevicePollDefaultInterval is used when reporting a pending device-code
// poll. KiroOAuthService.PollDeviceAuth discards the session on
// ErrAuthorizationPending/ErrSlowDown (it only returns the error, not the
// *kiro.OAuthSession), so the handler has no way to read back the exact
// interval AWS originally handed out in StartDeviceAuthorization. 5 seconds
// matches AWS's own device-authorization default (see
// kiro.StartDeviceAuthorization) and the value already returned to the
// frontend by DeviceStart, which the frontend should already be honoring for
// its own polling cadence — this is only a hint for callers that ignore that.
const kiroDevicePollDefaultInterval = 5

// KiroOAuthHandler 承载 Kiro（Amazon Q Developer / CodeWhisperer）账号的
// 授权流程：IdC 授权码（浏览器整页跳转到 Callback）与 Builder ID 设备码
// （轮询）两条路径，外加 IdC 流程专用的 FetchCredentials 中转读取端点。
type KiroOAuthHandler struct {
	svc *service.KiroOAuthService
}

// NewKiroOAuthHandler 创建 handler。
func NewKiroOAuthHandler(svc *service.KiroOAuthService) *KiroOAuthHandler {
	return &KiroOAuthHandler{svc: svc}
}

type kiroAuthorizeURLRequest struct {
	ProxyID     *int64 `json:"proxy_id"`
	RedirectURI string `json:"redirect_uri"`
	IssuerURL   string `json:"issuer_url"`
	Region      string `json:"region"`
}

// AuthorizeURL 发起 IdC 授权码流程：动态注册客户端 + PKCE，返回跳转地址。
// 必填参数校验交给 svc.GenerateAuthURL（唯一的校验来源，避免 handler/service
// 两处各写一份、报错文案还不一致）。
func (h *KiroOAuthHandler) AuthorizeURL(c *gin.Context) {
	var req kiroAuthorizeURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		req = kiroAuthorizeURLRequest{}
	}
	result, err := h.svc.GenerateAuthURL(c.Request.Context(), &service.KiroAuthURLInput{
		ProxyID:     req.ProxyID,
		RedirectURI: req.RedirectURI,
		IssuerURL:   req.IssuerURL,
		Region:      req.Region,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// Callback 是 AWS SSO 门户完成登录后浏览器直接落地的地址——这是给人看的
// HTML 页面，不是 JSON API。绝不能把 code / client_secret 回显到页面上。
//
// 成功兑换后把结果暂存进一次性凭据存储（StashCredentials），供管理员在
// 面板里点「我已完成授权」时通过 FetchCredentials 轮询取回——IdC 走浏览器
// 整页跳转完成回调，前端 JS 从始至终拿不到 AWS 回传的 code，这是唯一能把
// 兑换结果带回前端的路径。
func (h *KiroOAuthHandler) Callback(c *gin.Context) {
	if errParam := c.Query("error"); errParam != "" {
		h.renderCallbackError(c, http.StatusBadRequest, errParam, c.Query("error_description"))
		return
	}

	code := c.Query("code")
	state := c.Query("state")
	// AWS SSO 的授权回调只会原样带上 code + state——它不知道、也不会带上
	// sub2api 自己的 session_id 查询参数，真实回调里 session_id 永远是空的
	// （C2）。GenerateAuthURL 已经把 session_id 编码进了 state，这里优先
	// 兼容显式传入的 session_id（测试/未来可能的调用方），缺失时从 state
	// 里还原。
	sessionID := c.Query("session_id")
	if sessionID == "" {
		sessionID = h.svc.SessionIDFromState(state)
	}
	if sessionID == "" || code == "" {
		h.renderCallbackError(c, http.StatusBadRequest, "invalid_request", "missing session_id or authorization code")
		return
	}

	ts, sess, err := h.svc.ExchangeCode(c.Request.Context(), &service.KiroExchangeCodeInput{
		SessionID: sessionID,
		Code:      code,
		State:     state,
	})
	if err != nil {
		statusCode, status := infraerrors.ToHTTP(err)
		h.renderCallbackError(c, statusCode, status.Reason, status.Message)
		return
	}

	creds := h.svc.BuildAccountCredentials(service.KiroCredentialInput{
		TokenSet:     ts,
		Method:       sess.Method,
		Region:       sess.Region,
		IssuerURL:    sess.IssuerURL,
		ClientID:     sess.ClientID,
		ClientSecret: sess.ClientSecret,
	})
	h.svc.StashCredentials(c.Request.Context(), sessionID, creds)

	h.renderCallbackSuccess(c)
}

func (h *KiroOAuthHandler) renderCallbackSuccess(c *gin.Context) {
	body := `<!doctype html><html><head><meta charset="utf-8"><title>Kiro 授权成功</title></head>` +
		`<body style="font-family:-apple-system,BlinkMacSystemFont,Segoe UI,sans-serif;padding:32px;">` +
		`<h1>授权成功</h1><p>请回到管理后台完成账号创建，此页面可以关闭。</p></body></html>`
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(body))
}

// renderCallbackError 渲染错误原因，只展示错误码与人类可读原因——绝不把
// code / client_secret 等敏感值带进页面。
func (h *KiroOAuthHandler) renderCallbackError(c *gin.Context, statusCode int, reason, message string) {
	body := `<!doctype html><html><head><meta charset="utf-8"><title>Kiro 授权失败</title></head>` +
		`<body style="font-family:-apple-system,BlinkMacSystemFont,Segoe UI,sans-serif;padding:32px;">` +
		`<h1>授权失败</h1><p>` + html.EscapeString(reason)
	if message != "" {
		body += "：" + html.EscapeString(message)
	}
	body += `</p><p>请回到管理后台重试。</p></body></html>`
	c.Data(statusCode, "text/html; charset=utf-8", []byte(body))
}

type kiroDeviceStartRequest struct {
	ProxyID *int64 `json:"proxy_id"`
	Region  string `json:"region"`
}

// DeviceStart 发起 Builder ID 设备码授权。
func (h *KiroOAuthHandler) DeviceStart(c *gin.Context) {
	var req kiroDeviceStartRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		req = kiroDeviceStartRequest{}
	}
	result, err := h.svc.StartDeviceAuth(c.Request.Context(), req.ProxyID, req.Region)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

type kiroDevicePollRequest struct {
	SessionID string `json:"session_id" binding:"required"`
	ProxyID   *int64 `json:"proxy_id"`
}

// DevicePoll 轮询设备码。pending 不是错误——返回 200 + {"status":"pending",
// "interval":N}，让前端按 interval 继续轮询；只有终态失败才返回 4xx/5xx。
func (h *KiroOAuthHandler) DevicePoll(c *gin.Context) {
	var req kiroDevicePollRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	ts, sess, err := h.svc.PollDeviceAuth(c.Request.Context(), req.SessionID, req.ProxyID)
	if err != nil {
		if errors.Is(err, kiro.ErrAuthorizationPending) || errors.Is(err, kiro.ErrSlowDown) {
			response.Success(c, gin.H{"status": "pending", "interval": kiroDevicePollDefaultInterval})
			return
		}
		response.ErrorFrom(c, err)
		return
	}

	creds := h.svc.BuildAccountCredentials(service.KiroCredentialInput{
		TokenSet:     ts,
		Method:       sess.Method,
		Region:       sess.Region,
		IssuerURL:    sess.IssuerURL,
		ClientID:     sess.ClientID,
		ClientSecret: sess.ClientSecret,
	})
	response.Success(c, gin.H{"status": "ok", "credentials": creds})
}

// FetchCredentials 供管理员点「我已完成授权」后轮询取回 IdC 回调暂存的
// credentials（一次性）。找不到——尚未到达 / 已被消费 / 已过期——三者
// 无法区分，统一按 pending 处理，200 而不是 404/error，允许前端继续轮询。
func (h *KiroOAuthHandler) FetchCredentials(c *gin.Context) {
	sessionID := c.Param("session_id")
	creds, ok := h.svc.TakeStashedCredentials(c.Request.Context(), sessionID)
	if !ok {
		response.Success(c, gin.H{"status": "pending"})
		return
	}
	response.Success(c, gin.H{"status": "ok", "credentials": creds})
}
