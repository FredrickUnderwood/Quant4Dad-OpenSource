package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/service"
)

type AgentModelProbeHandler struct {
	app *application.AgentModelProbeApplication
}

func NewAgentModelProbeHandler(app *application.AgentModelProbeApplication) *AgentModelProbeHandler {
	return &AgentModelProbeHandler{app: app}
}
func (h *AgentModelProbeHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/agent/models/:provider/probe", h.probe)
}
func (h *AgentModelProbeHandler) probe(c *gin.Context) {
	modelHeaders(c)
	if c.Request.URL.RawQuery != "" || c.Request.URL.ForceQuery || c.GetHeader("Content-Encoding") != "" {
		modelHTTPError(c, service.ErrModelInputInvalid)
		return
	}
	var request struct {
		Force bool `json:"force"`
	}
	if !decodeModelBody(c, &request) {
		return
	}
	result, err := h.app.Probe(c.Request.Context(), c.Param("provider"), request.Force)
	if err != nil {
		if errors.Is(err, service.ErrAgentProbeUnavailable) {
			c.JSON(http.StatusServiceUnavailable, ErrorResponse{Code: 503, Message: err.Error()})
		} else if errors.Is(err, service.ErrAgentProbeConfig) {
			c.JSON(http.StatusUnprocessableEntity, ErrorResponse{Code: 422, Message: err.Error()})
		} else {
			modelHTTPError(c, err)
		}
		return
	}
	ok(c, result)
}
