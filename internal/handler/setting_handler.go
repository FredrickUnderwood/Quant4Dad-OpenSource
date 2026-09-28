package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/service"
)

type SettingHandler struct {
	svc *service.SettingService
}

func NewSettingHandler(svc *service.SettingService) *SettingHandler {
	return &SettingHandler{svc: svc}
}

func (h *SettingHandler) Register(rg *gin.RouterGroup) {
	g := rg.Group("/settings")
	// LLM provider settings: reads are redacted, never returning the api_key, and a write with
	// an empty key keeps the stored one.
	g.GET("/llm-providers", h.getLLMProviders)
	g.PUT("/llm-providers", h.setLLMProviders)
	// Delivery channel settings: reads are redacted, never returning the password or secret,
	// and a write with an empty credential keeps the stored one.
	g.GET("/notify-email", h.getNotifyEmail)
	g.PUT("/notify-email", h.setNotifyEmail)
	g.GET("/notify-feishu", h.getNotifyFeishu)
	g.PUT("/notify-feishu", h.setNotifyFeishu)
}

func (h *SettingHandler) getLLMProviders(c *gin.Context) {
	modelHeaders(c)
	view, err := h.svc.GetLLMProvidersView(c.Request.Context())
	if err != nil {
		modelHTTPError(c, err)
		return
	}
	writeModelView(c, view)
}

type setLLMProvidersRequest struct {
	Providers map[string]service.LLMProvider `json:"providers"`
}

func (h *SettingHandler) setLLMProviders(c *gin.Context) {
	modelHeaders(c)
	var req setLLMProvidersRequest
	if !decodeModelBody(c, &req) {
		return
	}
	expected, valid := modelIfMatch(c)
	if !valid {
		return
	}
	view, err := h.svc.ReplaceLLMProviders(c.Request.Context(), req.Providers, expected)
	if err != nil {
		modelHTTPError(c, err)
		return
	}
	writeModelView(c, view)
}

// ---- Delivery channel: email (SMTP) ----

func (h *SettingHandler) getNotifyEmail(c *gin.Context) {
	cfg, err := h.svc.GetEmailSettingMasked(c.Request.Context())
	if err != nil {
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, gin.H{"email": cfg})
}

func (h *SettingHandler) setNotifyEmail(c *gin.Context) {
	var req service.EmailSetting
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: err.Error()})
		return
	}
	if err := h.svc.SetEmailSetting(c.Request.Context(), req); err != nil {
		logger.L().Error("set notify email failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	cfg, err := h.svc.GetEmailSettingMasked(c.Request.Context())
	if err != nil {
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, gin.H{"email": cfg})
}

// ---- Delivery channel: Feishu ----

func (h *SettingHandler) getNotifyFeishu(c *gin.Context) {
	cfg, err := h.svc.GetFeishuSettingMasked(c.Request.Context())
	if err != nil {
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, gin.H{"feishu": cfg})
}

func (h *SettingHandler) setNotifyFeishu(c *gin.Context) {
	var req service.FeishuSetting
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: err.Error()})
		return
	}
	if err := h.svc.SetFeishuSetting(c.Request.Context(), req); err != nil {
		logger.L().Error("set notify feishu failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	cfg, err := h.svc.GetFeishuSettingMasked(c.Request.Context())
	if err != nil {
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, gin.H{"feishu": cfg})
}
