package handler

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/service"
	"time"
)

type AgentToolResultHandler struct {
	app *application.AgentToolResultApplication
}

func NewAgentToolResultHandler(app *application.AgentToolResultApplication) *AgentToolResultHandler {
	return &AgentToolResultHandler{app: app}
}
func (h *AgentToolResultHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/agent/runs/:runID/tools/:toolCallID", func(c *gin.Context) {
		modelHeaders(c)
		if !sessionNoQuery(c) {
			return
		}
		if c.Request.ContentLength != 0 || len(c.Request.TransferEncoding) > 0 || c.GetHeader("Content-Encoding") != "" {
			runHTTPError(c, service.ErrAgentRunInput, "")
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		result, err := h.app.Get(ctx, agentLocalActor, c.Param("runID"), c.Param("toolCallID"))
		if err != nil {
			runHTTPError(c, err, "")
			return
		}
		c.JSON(200, result)
	})
}
