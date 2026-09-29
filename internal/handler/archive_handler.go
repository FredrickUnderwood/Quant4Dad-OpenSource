package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/logger"
)

// ArchiveHandler exposes destructive event archiving. Connection probes use a
// separate read-only settings endpoint and never call RunOnce.
type archiveRunner interface {
	Status() application.ArchiveStatus
	RunOnce(context.Context) (int, int64, error)
}
type ArchiveHandler struct {
	scheduler archiveRunner
}

func NewArchiveHandler(scheduler archiveRunner) *ArchiveHandler {
	return &ArchiveHandler{scheduler: scheduler}
}

func (h *ArchiveHandler) Register(rg *gin.RouterGroup) {
	g := rg.Group("/archive")
	g.GET("/status", h.status)
	g.POST("/run", h.run)
}

func (h *ArchiveHandler) status(c *gin.Context) {
	if manager, ok := h.scheduler.(interface {
		ManagedStatus() application.ManagedArchiveStatus
	}); ok {
		okResponse := manager.ManagedStatus()
		c.JSON(http.StatusOK, okResponse)
		return
	}
	ok(c, h.scheduler.Status())
}

// run triggers one round of archiving synchronously and returns how many days and events it
// covered, or 409 when a round is already in progress. With a lot of data the request blocks
// for a while, which is acceptable for local verification.
func (h *ArchiveHandler) run(c *gin.Context) {
	days, events, err := h.scheduler.RunOnce(c.Request.Context())
	if err != nil {
		if errors.Is(err, application.ErrArchiveBusy) {
			c.JSON(http.StatusConflict, ErrorResponse{Code: 409, Message: err.Error()})
			return
		}
		if errors.Is(err, application.ErrArchiveUnconfigured) {
			c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: "请先在设置中配置 OSS 归档"})
			return
		}
		logger.L().Error("manual archive run failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, gin.H{"days": days, "events": events})
}
