package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/service"
)

type CostHandler struct {
	svc *service.CostService
}

func NewCostHandler(svc *service.CostService) *CostHandler {
	return &CostHandler{svc: svc}
}

func (h *CostHandler) Register(rg *gin.RouterGroup) {
	g := rg.Group("/costs")
	g.GET("", h.list)
	g.POST("", h.create)
	g.GET("/:id", h.get)
	g.PUT("/:id", h.update)
	g.DELETE("/:id", h.delete)
	g.POST("/:id/default", h.setDefault)
}

func (h *CostHandler) list(c *gin.Context) {
	items, err := h.svc.List(c.Request.Context())
	if err != nil {
		logger.L().Error("cost list failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, items)
}

func (h *CostHandler) create(c *gin.Context) {
	var in domain.Cost
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: err.Error()})
		return
	}
	if err := h.svc.Create(c.Request.Context(), &in); err != nil {
		logger.L().Error("cost create failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	created(c, &in)
}

func (h *CostHandler) update(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	var in domain.Cost
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: err.Error()})
		return
	}
	in.ID = id
	if err := h.svc.Update(c.Request.Context(), &in); err != nil {
		logger.L().Error("cost update failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, &in)
}

func (h *CostHandler) get(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	cst, err := h.svc.GetByID(c.Request.Context(), id)
	if err != nil {
		logger.L().Error("cost get failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, cst)
}

func (h *CostHandler) delete(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		logger.L().Error("cost delete failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *CostHandler) setDefault(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if err := h.svc.SetDefault(c.Request.Context(), id); err != nil {
		logger.L().Error("cost set default failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	c.Status(http.StatusNoContent)
}
