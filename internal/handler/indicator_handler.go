package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/quant4dad/internal/service"
)

type IndicatorHandler struct {
	svc *service.IndicatorService
}

func NewIndicatorHandler(svc *service.IndicatorService) *IndicatorHandler {
	return &IndicatorHandler{svc: svc}
}

func (h *IndicatorHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/indicators", h.list)
}

func (h *IndicatorHandler) list(c *gin.Context) {
	ok(c, h.svc.List())
}
