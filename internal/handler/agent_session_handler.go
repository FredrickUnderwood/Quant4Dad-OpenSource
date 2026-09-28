package handler

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/quant4dad/internal/agentbridge"
	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/service"
)

// The existing authenticated installation has one local owner, not a user
// directory. Never take actor identity from a browser header, body or query.
const agentLocalActor = "local-user"

type AgentSessionHandler struct {
	app *application.AgentSessionApplication
}

func NewAgentSessionHandler(app *application.AgentSessionApplication) *AgentSessionHandler {
	return &AgentSessionHandler{app: app}
}
func (h *AgentSessionHandler) Register(rg *gin.RouterGroup) {
	routes := rg.Group("/agent/sessions")
	routes.Use(func(c *gin.Context) {
		modelHeaders(c)
		if c.GetHeader("Content-Encoding") != "" || len(c.Request.URL.RawQuery) > 1024 ||
			(c.Request.Method == http.MethodGet && (c.Request.ContentLength != 0 || len(c.Request.TransferEncoding) != 0)) {
			sessionHTTPError(c, service.ErrAgentSessionInput)
			c.Abort()
		}
	})
	routes.POST("", h.create)
	routes.GET("", h.list)
	routes.GET("/:sessionID", h.detail)
	routes.GET("/:sessionID/metadata", h.metadata)
	routes.PATCH("/:sessionID", h.patch)
	routes.POST("/:sessionID/reconcile", h.reconcile)
}
func sessionHTTPError(c *gin.Context, err error) {
	status, message := 500, "internal server error"
	switch {
	case errors.Is(err, service.ErrAgentSessionInput):
		status, message = 400, err.Error()
	case errors.Is(err, service.ErrAgentSessionNotFound):
		status, message = 404, err.Error()
	case errors.Is(err, service.ErrAgentSessionConflict), errors.Is(err, service.ErrAgentSessionState):
		status, message = 409, err.Error()
	case errors.Is(err, service.ErrAgentSessionProfile), errors.Is(err, service.ErrAgentSessionModel):
		status, message = 422, err.Error()
	case errors.Is(err, service.ErrAgentSessionUnavailable):
		status, message = 503, err.Error()
	}
	c.JSON(status, ErrorResponse{Code: status, Message: message})
}
func sessionNoQuery(c *gin.Context) bool {
	if c.Request.URL.RawQuery != "" || c.Request.URL.ForceQuery {
		sessionHTTPError(c, service.ErrAgentSessionInput)
		return false
	}
	return true
}
func sessionQuery(c *gin.Context, allowed ...string) (url.Values, bool) {
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		sessionHTTPError(c, service.ErrAgentSessionInput)
		return nil, false
	}
	for key, values := range query {
		ok := false
		for _, name := range allowed {
			if key == name {
				ok = true
			}
		}
		if !ok || len(values) != 1 || values[0] == "" {
			sessionHTTPError(c, service.ErrAgentSessionInput)
			return nil, false
		}
	}
	return query, true
}
func sessionLimit(c *gin.Context, q url.Values, def int) (int, bool) {
	value := q.Get("limit")
	if value == "" {
		return def, true
	}
	limit, err := strconv.Atoi(value)
	if err != nil || strconv.Itoa(limit) != value || limit < 1 || limit > 100 {
		sessionHTTPError(c, service.ErrAgentSessionInput)
		return 0, false
	}
	return limit, true
}
func (h *AgentSessionHandler) create(c *gin.Context) {
	if !sessionNoQuery(c) {
		return
	}
	keys := c.Request.Header.Values("Idempotency-Key")
	if len(keys) != 1 {
		sessionHTTPError(c, service.ErrAgentSessionInput)
		return
	}
	var request service.AgentSessionCreate
	if !decodeModelBody(c, &request) {
		return
	}
	value, err := h.app.Create(c.Request.Context(), agentLocalActor, keys[0], request)
	if err != nil {
		sessionHTTPError(c, err)
		return
	}
	status := http.StatusAccepted
	if value.Status == domain.AgentSessionActive || value.Status == domain.AgentSessionArchived {
		status = http.StatusCreated
	}
	c.Header("Location", "/api/v1/agent/sessions/"+value.SessionID)
	c.JSON(status, value)
}
func (h *AgentSessionHandler) list(c *gin.Context) {
	q, ok := sessionQuery(c, "limit", "cursor")
	if !ok {
		return
	}
	limit, ok := sessionLimit(c, q, 20)
	if !ok {
		return
	}
	value, err := h.app.List(c.Request.Context(), agentLocalActor, q.Get("cursor"), limit)
	if err != nil {
		sessionHTTPError(c, err)
		return
	}
	c.JSON(200, value)
}
func (h *AgentSessionHandler) detail(c *gin.Context) {
	q, ok := sessionQuery(c, "limit", "before_seq", "snapshot_seq")
	if !ok {
		return
	}
	limit, ok := sessionLimit(c, q, 100)
	if !ok {
		return
	}
	value, err := h.app.Detail(c.Request.Context(), agentLocalActor, c.Param("sessionID"), agentbridge.TranscriptQuery{BeforeSeq: q.Get("before_seq"), SnapshotSeq: q.Get("snapshot_seq"), Limit: limit})
	if err != nil {
		sessionHTTPError(c, err)
		return
	}
	c.JSON(200, value)
}
func (h *AgentSessionHandler) patch(c *gin.Context) {
	if !sessionNoQuery(c) {
		return
	}
	var patch service.AgentSessionPatch
	if !decodeModelBody(c, &patch) {
		return
	}
	value, err := h.app.Patch(c.Request.Context(), agentLocalActor, c.Param("sessionID"), patch)
	if err != nil {
		sessionHTTPError(c, err)
		return
	}
	c.JSON(200, value)
}
func (h *AgentSessionHandler) metadata(c *gin.Context) {
	if !sessionNoQuery(c) {
		return
	}
	value, err := h.app.Metadata(c.Request.Context(), agentLocalActor, c.Param("sessionID"))
	if err != nil {
		sessionHTTPError(c, err)
		return
	}
	c.JSON(http.StatusOK, value)
}
func (h *AgentSessionHandler) reconcile(c *gin.Context) {
	if !sessionNoQuery(c) {
		return
	}
	var request struct{}
	if !decodeModelBody(c, &request) {
		return
	}
	value, err := h.app.Reconcile(c.Request.Context(), agentLocalActor, c.Param("sessionID"))
	if err != nil {
		sessionHTTPError(c, err)
		return
	}
	status := http.StatusAccepted
	if value.Status == domain.AgentSessionActive || value.Status == domain.AgentSessionArchived {
		status = http.StatusOK
	}
	c.JSON(status, value)
}
