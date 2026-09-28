package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
)

type InstrumentHandler struct {
	svc *service.InstrumentService
}

func NewInstrumentHandler(svc *service.InstrumentService) *InstrumentHandler {
	return &InstrumentHandler{svc: svc}
}

func (h *InstrumentHandler) Register(rg *gin.RouterGroup) {
	g := rg.Group("/instruments")
	g.GET("", h.list)
	g.GET("/:code", h.get)
	g.GET("/:code/bars", h.listBars)
}

func (h *InstrumentHandler) list(c *gin.Context) {
	keyword := c.Query("keyword")
	page, _ := strconv.Atoi(c.Query("page"))
	size, _ := strconv.Atoi(c.Query("size"))
	assetType := domain.AssetType(c.Query("asset_type"))
	if assetType != "" && assetType != domain.AssetStock && assetType != domain.AssetETF {
		c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: "invalid asset_type"})
		return
	}
	items, total, err := h.svc.List(c.Request.Context(), repository.ListInstrumentsFilter{
		Keyword: keyword, Page: page, Size: size, AssetType: assetType,
	})
	if err != nil {
		logger.L().Error("instrument list failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, gin.H{"items": items, "total": total})
}

func (h *InstrumentHandler) get(c *gin.Context) {
	code := c.Param("code")
	it, err := h.svc.GetByCode(c.Request.Context(), code)
	if err != nil {
		logger.L().Error("instrument get failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, it)
}

func (h *InstrumentHandler) listBars(c *gin.Context) {
	code := c.Param("code")
	period := domain.BarPeriod(c.DefaultQuery("period", "1d"))
	var start, end time.Time
	if s := c.Query("start"); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: "invalid start"})
			return
		}
		start = t
	}
	if s := c.Query("end"); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: "invalid end"})
			return
		}
		end = t
	}
	bars, err := h.svc.RangeBars(c.Request.Context(), code, period, start, end)
	if err != nil {
		logger.L().Error("instrument bars failed", zap.String("code", code), zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, bars)
}
