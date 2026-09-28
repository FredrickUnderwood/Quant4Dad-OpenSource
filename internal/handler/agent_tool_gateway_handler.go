package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/mcp"
)

type AgentToolGatewayHandler struct{ handler http.Handler }

func NewAgentToolGatewayHandler(app *application.AgentToolGatewayApplication, token string) *AgentToolGatewayHandler {
	if app == nil || token == "" {
		return nil
	}
	return &AgentToolGatewayHandler{mcp.InternalHTTPHandler(app, token)}
}
func (h *AgentToolGatewayHandler) Register(e *gin.Engine) {
	e.Any("/internal/mcp", gin.WrapH(h.handler))
}
