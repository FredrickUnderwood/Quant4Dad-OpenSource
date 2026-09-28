package handler

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/service"
)

const agentBootstrapPath = "/internal/v1/agent/bootstrap"

type AgentBootstrapHandler struct {
	app       *application.AgentBootstrapApplication
	tokenHash [32]byte
}

func NewAgentBootstrapHandler(app *application.AgentBootstrapApplication, controlToken string) *AgentBootstrapHandler {
	if app == nil || controlToken == "" {
		return nil
	}
	return &AgentBootstrapHandler{app: app, tokenHash: sha256.Sum256([]byte(controlToken))}
}

func agentControlRequest(c *gin.Context) bool {
	return strings.HasPrefix(c.Request.URL.Path, "/internal/v1/agent") || strings.HasPrefix(c.Request.URL.Path, "/internal/mcp") || strings.HasPrefix(c.Request.URL.Path, "/external/mcp")
}

// Control requests need an access logger that cannot echo arbitrary query,
// path suffixes, user-agent, cookies, or Authorization into log files.
func agentControlAccessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !agentControlRequest(c) {
			c.Next()
			return
		}
		start := time.Now()
		c.Header("Cache-Control", "no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Next()
		logger.L().Info("agent control request", zap.Int("status", c.Writer.Status()), zap.Duration("latency", time.Since(start)))
	}
}

func (h *AgentBootstrapHandler) Register(e *gin.Engine) {
	e.GET(agentBootstrapPath, h.authenticate, h.get)
}

func (h *AgentBootstrapHandler) authenticate(c *gin.Context) {
	values := c.Request.Header.Values("Authorization")
	if len(values) == 1 && strings.HasPrefix(values[0], "Bearer ") && len(values[0]) <= 263 {
		digest := sha256.Sum256([]byte(strings.TrimPrefix(values[0], "Bearer ")))
		if subtle.ConstantTimeCompare(digest[:], h.tokenHash[:]) == 1 {
			c.Next()
			return
		}
	}
	// Cookie auth and credentials in query/body are never a fallback.
	c.Header("WWW-Authenticate", "Bearer")
	c.AbortWithStatusJSON(http.StatusUnauthorized, ErrorResponse{Code: 401, Message: "unauthorized"})
}

func (h *AgentBootstrapHandler) get(c *gin.Context) {
	// Only one optional, nonempty revision is supported. ParseQuery errors and
	// duplicate/unknown parameters fail closed rather than silently dropping data.
	if len(c.Request.URL.RawQuery) > 128 || c.Request.ContentLength != 0 || len(c.Request.TransferEncoding) != 0 {
		modelHTTPError(c, service.ErrModelInputInvalid)
		return
	}
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil || len(query) > 1 {
		modelHTTPError(c, service.ErrModelInputInvalid)
		return
	}
	known := ""
	for name, values := range query {
		if name != "revision" || len(values) != 1 || values[0] == "" {
			modelHTTPError(c, service.ErrModelInputInvalid)
			return
		}
		known = values[0]
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	snapshot, unchanged, err := h.app.Read(ctx, known)
	if err != nil {
		modelHTTPError(c, err)
		return
	}
	if unchanged {
		c.Header("ETag", `"`+snapshot.Revision+`"`)
		c.Status(http.StatusNotModified)
		return
	}
	// Serialize before committing a successful status; parser/serializer errors
	// and private structures are never attached to Gin's error/access logger.
	// Older Runtime schemas reject unknown fields. Negotiate this additive
	// metadata so the API can be deployed before the Runtime is upgraded.
	if c.GetHeader("X-Q4D-Bootstrap-Profiles") != "1" {
		snapshot.Profiles = nil
	}
	body, err := sonic.Marshal(snapshot)
	if err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse{Code: 500, Message: "internal server error"})
		return
	}
	if len(body) > 1<<20 {
		modelHTTPError(c, service.ErrModelConfigInvalid)
		return
	}
	c.Header("ETag", `"`+snapshot.Revision+`"`)
	c.Data(http.StatusOK, "application/json; charset=utf-8", body)
}
