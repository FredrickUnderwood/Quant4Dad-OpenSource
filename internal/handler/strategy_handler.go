package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/service"
	"go.uber.org/zap"
)

type StrategyHandler struct {
	svc *service.StrategyService
}

func NewStrategyHandler(svc *service.StrategyService) *StrategyHandler {
	return &StrategyHandler{svc: svc}
}

func (h *StrategyHandler) Register(rg *gin.RouterGroup) {
	g := rg.Group("/strategies")
	g.GET("", h.list)
	g.POST("", h.create)
	g.GET("/:id", h.get)
	g.PUT("/:id", h.update)
	g.DELETE("/:id", h.delete)
}

func (h *StrategyHandler) list(c *gin.Context) {
	items, err := h.svc.List(c.Request.Context())
	if err != nil {
		logger.L().Error("strategy list failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, items)
}

func (h *StrategyHandler) create(c *gin.Context) {
	var in service.StrategyInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: err.Error()})
		return
	}
	st, err := h.svc.Create(c.Request.Context(), in)
	if err != nil {
		logger.L().Error("strategy create failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	created(c, st)
}

func (h *StrategyHandler) update(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	var in service.StrategyInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: err.Error()})
		return
	}
	st, err := h.svc.Update(c.Request.Context(), id, in)
	if err != nil {
		logger.L().Error("strategy update failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, st)
}

func (h *StrategyHandler) get(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	st, err := h.svc.GetByID(c.Request.Context(), id)
	if err != nil {
		logger.L().Error("strategy get failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, st)
}

func (h *StrategyHandler) delete(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		logger.L().Error("strategy delete failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	c.Status(http.StatusNoContent)
}

func parseID(c *gin.Context) (int64, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: "invalid id"})
		return 0, err
	}
	return id, nil
}
