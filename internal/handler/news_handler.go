package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
)

type NewsHandler struct {
	svc       *service.NewsService
	collector *application.NewsCollector
}

func NewNewsHandler(svc *service.NewsService, collector *application.NewsCollector) *NewsHandler {
	return &NewsHandler{svc: svc, collector: collector}
}

func (h *NewsHandler) Register(rg *gin.RouterGroup) {
	g := rg.Group("/news")
	// Static routes come before /:id, so the parameterized route does not swallow them.
	g.GET("/sources", h.listSources)
	g.PUT("/sources", h.setSources)
	g.POST("/sources/:source/poll", h.pollSource)
	g.GET("", h.list)
	g.GET("/:id", h.get)
}

func (h *NewsHandler) list(c *gin.Context) {
	q := repository.NewsQuery{
		Source:  c.Query("source"),
		Keyword: c.Query("keyword"),
	}
	if v := c.Query("limit"); v != "" {
		q.Limit, _ = strconv.Atoi(v)
	}
	if v := c.Query("offset"); v != "" {
		q.Offset, _ = strconv.Atoi(v)
	}
	items, total, err := h.svc.ListNews(c.Request.Context(), q)
	if err != nil {
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, gin.H{"total": total, "items": items})
}

func (h *NewsHandler) get(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: "invalid id"})
		return
	}
	n, err := h.svc.GetNews(c.Request.Context(), id)
	if err != nil {
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, n)
}

// listSources returns the collector's global config plus each source's collection status,
// covering both its config and its runtime state.
func (h *NewsHandler) listSources(c *gin.Context) {
	h.respondSources(c)
}

type setSourcesRequest struct {
	Enabled             bool                                 `json:"enabled"`
	BaseIntervalSeconds int                                  `json:"base_interval_seconds"`
	Sources             map[string]service.NewsSourceSetting `json:"sources"`
}

func (h *NewsHandler) setSources(c *gin.Context) {
	var req setSourcesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: err.Error()})
		return
	}
	ctx := c.Request.Context()
	if err := h.svc.SetCollectorConfig(ctx, service.NewsCollectorConfig{
		Enabled:             req.Enabled,
		BaseIntervalSeconds: req.BaseIntervalSeconds,
	}); err != nil {
		c.JSON(toHTTPError(err))
		return
	}
	if err := h.svc.SetSourceConfigs(ctx, req.Sources); err != nil {
		c.JSON(toHTTPError(err))
		return
	}
	h.respondSources(c)
}

// respondSources is the shared response: the global config plus each source's status.
func (h *NewsHandler) respondSources(c *gin.Context) {
	ctx := c.Request.Context()
	cc, err := h.svc.GetCollectorConfig(ctx)
	if err != nil {
		c.JSON(toHTTPError(err))
		return
	}
	st, err := h.collector.Status(ctx)
	if err != nil {
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, gin.H{
		"enabled":               cc.Enabled,
		"base_interval_seconds": cc.BaseIntervalSeconds,
		"sources":               st,
	})
}

// pollSource collects from the named source immediately and returns how many items were new.
func (h *NewsHandler) pollSource(c *gin.Context) {
	source := c.Param("source")
	n, err := h.collector.PollNow(c.Request.Context(), source)
	if err != nil {
		logger.L().Warn("manual news poll failed", zap.String("source", source), zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, gin.H{"source": source, "new": n})
}
