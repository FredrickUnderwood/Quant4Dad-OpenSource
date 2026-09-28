package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/service"
)

type BacktestHandler struct {
	svc *service.BacktestService
	app *application.BacktestApp
}

func NewBacktestHandler(svc *service.BacktestService, app *application.BacktestApp) *BacktestHandler {
	return &BacktestHandler{svc: svc, app: app}
}

func (h *BacktestHandler) Register(rg *gin.RouterGroup) {
	g := rg.Group("/backtests")
	g.GET("", h.listJobs)
	g.POST("", h.create)
	g.GET("/:id", h.getJob)
	g.GET("/:id/result", h.getResult)
	g.GET("/:id/trades", h.listTrades)
	g.GET("/:id/equity", h.listEquity)
}

type createBacktestRequest struct {
	StrategyID     int64   `json:"strategy_id"     binding:"required"`
	CostID         int64   `json:"cost_id"`
	InitialCapital float64 `json:"initial_capital" binding:"required"`
	StartDate      string  `json:"start_date"      binding:"required"` // YYYY-MM-DD
	EndDate        string  `json:"end_date"        binding:"required"`
}

func (h *BacktestHandler) create(c *gin.Context) {
	var req createBacktestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: err.Error()})
		return
	}
	start, err := time.Parse("2006-01-02", req.StartDate)
	if err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: "invalid start_date"})
		return
	}
	end, err := time.Parse("2006-01-02", req.EndDate)
	if err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: "invalid end_date"})
		return
	}
	job := &domain.BacktestJob{
		StrategyID:     req.StrategyID,
		CostID:         req.CostID,
		InitialCapital: req.InitialCapital,
		StartDate:      start,
		EndDate:        end,
	}
	created, err := h.app.Enqueue(c.Request.Context(), job)
	if err != nil {
		logger.L().Error("backtest enqueue failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	c.JSON(http.StatusAccepted, created)
}

func (h *BacktestHandler) listJobs(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	items, err := h.svc.ListJobs(c.Request.Context(), limit)
	if err != nil {
		logger.L().Error("backtest list failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, items)
}

func (h *BacktestHandler) getJob(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	job, err := h.svc.GetJob(c.Request.Context(), id)
	if err != nil {
		logger.L().Error("backtest get failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, job)
}

func (h *BacktestHandler) getResult(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	res, err := h.svc.GetResult(c.Request.Context(), id)
	if err != nil {
		logger.L().Error("backtest result failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, res)
}

func (h *BacktestHandler) listTrades(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	items, err := h.svc.ListTrades(c.Request.Context(), id)
	if err != nil {
		logger.L().Error("backtest trades failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, items)
}

func (h *BacktestHandler) listEquity(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	items, err := h.svc.ListEquity(c.Request.Context(), id)
	if err != nil {
		logger.L().Error("backtest equity failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, items)
}
