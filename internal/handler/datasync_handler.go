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

type DataSyncHandler struct {
	svc      *service.DataSyncService
	app      *application.DataSyncApp
	coverage *application.CoverageScanner
	auto     *application.AutoSyncScheduler
}

func NewDataSyncHandler(
	svc *service.DataSyncService,
	app *application.DataSyncApp,
	coverage *application.CoverageScanner,
	auto *application.AutoSyncScheduler,
) *DataSyncHandler {
	return &DataSyncHandler{svc: svc, app: app, coverage: coverage, auto: auto}
}

func (h *DataSyncHandler) Register(rg *gin.RouterGroup) {
	g := rg.Group("/datasync")
	g.GET("", h.list)
	g.POST("", h.create)
	g.GET("/:id", h.get)
	g.GET("/:id/failures", h.failures)
	g.GET("/coverage", h.coverageList)
	g.POST("/coverage/scan", h.coverageScan)
	g.GET("/auto", h.autoStatus)
}

type createDataSyncRequest struct {
	Mode      domain.DataSyncMode `json:"mode"`   // full / incremental
	Period    domain.BarPeriod    `json:"period"` // 1d / 1w / 1mo
	Codes     []string            `json:"codes"`  // empty = all
	StartDate string              `json:"start_date"`
	EndDate   string              `json:"end_date"`
}

func (h *DataSyncHandler) create(c *gin.Context) {
	var req createDataSyncRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: err.Error()})
		return
	}
	task := &domain.DataSyncTask{
		Mode:   req.Mode,
		Period: req.Period,
		Codes:  domain.StringSlice(req.Codes),
	}
	if req.StartDate != "" {
		t, err := time.Parse("2006-01-02", req.StartDate)
		if err != nil {
			c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: "invalid start_date"})
			return
		}
		task.StartDate = t
	}
	if req.EndDate != "" {
		t, err := time.Parse("2006-01-02", req.EndDate)
		if err != nil {
			c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: "invalid end_date"})
			return
		}
		task.EndDate = t
	}
	out, err := h.app.Enqueue(c.Request.Context(), task)
	if err != nil {
		logger.L().Error("datasync enqueue failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	c.JSON(http.StatusAccepted, out)
}

func (h *DataSyncHandler) list(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	items, err := h.svc.List(c.Request.Context(), limit)
	if err != nil {
		logger.L().Error("datasync list failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, items)
}

func (h *DataSyncHandler) get(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	t, err := h.svc.GetByID(c.Request.Context(), id)
	if err != nil {
		logger.L().Error("datasync get failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, t)
}

// failures returns the top N (default 5) failed cases of a sync task, including
// the raw provider response, to support analysis from the frontend.
func (h *DataSyncHandler) failures(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	if limit <= 0 {
		limit = 5
	}
	items, err := h.svc.ListFailures(c.Request.Context(), id, limit)
	if err != nil {
		logger.L().Error("datasync failures list failed", zap.Int64("id", id), zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, items)
}

func (h *DataSyncHandler) coverageList(c *gin.Context) {
	if h.coverage == nil {
		c.JSON(http.StatusServiceUnavailable, ErrorResponse{Code: 503, Message: "coverage scanner disabled"})
		return
	}
	items, err := h.coverage.List(c.Request.Context())
	if err != nil {
		logger.L().Error("coverage list failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, gin.H{"items": items, "running": h.coverage.IsRunning()})
}

func (h *DataSyncHandler) coverageScan(c *gin.Context) {
	if h.coverage == nil {
		c.JSON(http.StatusServiceUnavailable, ErrorResponse{Code: 503, Message: "coverage scanner disabled"})
		return
	}
	if err := h.coverage.TriggerAsync(); err != nil {
		if err == application.ErrCoverageScanBusy {
			c.JSON(http.StatusConflict, ErrorResponse{Code: 409, Message: "scan already running"})
			return
		}
		logger.L().Error("coverage trigger failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"running": true})
}

func (h *DataSyncHandler) autoStatus(c *gin.Context) {
	if h.auto == nil {
		ok(c, application.AutoSyncStatus{Enabled: false})
		return
	}
	ok(c, h.auto.Status())
}
