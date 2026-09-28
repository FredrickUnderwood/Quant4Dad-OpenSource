package handler

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"time"

	"github.com/bytedance/sonic"
	"github.com/gin-gonic/gin"
	"github.com/quant4dad/internal/agentbridge"
	"github.com/quant4dad/internal/agentrunauth"
	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/service"
)

type AgentRunHandler struct {
	app     *application.AgentRunApplication
	control *AgentBootstrapHandler
}

func NewAgentRunHandler(app *application.AgentRunApplication, controlToken string) *AgentRunHandler {
	if app == nil || controlToken == "" {
		return nil
	}
	return &AgentRunHandler{app, &AgentBootstrapHandler{tokenHash: sha256.Sum256([]byte(controlToken))}}
}
func (h *AgentRunHandler) Register(rg *gin.RouterGroup) {
	routes := rg.Group("/agent")
	routes.Use(func(c *gin.Context) {
		modelHeaders(c)
		if c.GetHeader("Content-Encoding") != "" || !sessionNoQuery(c) || (c.Request.Method == "GET" && (c.Request.ContentLength != 0 || len(c.Request.TransferEncoding) != 0)) {
			if !c.Writer.Written() {
				runHTTPError(c, service.ErrAgentRunInput, "")
			}
			c.Abort()
		}
	})
	routes.POST("/sessions/:sessionID/messages", h.send)
	routes.GET("/runs/:runID", h.get)
	routes.GET("/runs/:runID/events", h.events)
	routes.POST("/runs/:runID/cancel", h.cancel)
	routes.POST("/runs/:runID/reconcile", h.reconcile)
	routes.GET("/options", func(c *gin.Context) { c.JSON(200, gin.H{"profiles": h.app.Profiles(), "text_only": h.app.TextOnly()}) })
	routes.GET("/status", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		c.JSON(200, h.app.RuntimeStatus(ctx))
	})
}
func (h *AgentRunHandler) RegisterControl(e *gin.Engine) {
	e.GET("/internal/v1/agent/runs/:runID/authorization", h.control.authenticate, h.authorization)
}
func runHTTPError(c *gin.Context, err error, id string) {
	status, code := 500, "internal server error"
	switch {
	case errors.Is(err, service.ErrAgentRunCursorInvalid):
		status, code = 400, err.Error()
	case errors.Is(err, service.ErrAgentRunCursorExpired):
		status, code = 410, err.Error()
	case errors.Is(err, service.ErrAgentRunInput):
		status, code = 400, err.Error()
	case errors.Is(err, service.ErrAgentRunNotFound), errors.Is(err, service.ErrAgentToolNotFound):
		status, code = 404, err.Error()
	case errors.Is(err, service.ErrAgentRunConflict), errors.Is(err, service.ErrAgentRunBusy):
		status, code = 409, err.Error()
	case errors.Is(err, service.ErrAgentRunRejected), errors.Is(err, service.ErrAgentRunExpired):
		status, code = 403, err.Error()
	case errors.Is(err, service.ErrAgentRunPending), errors.Is(err, service.ErrAgentRunUnavailable):
		status, code = 503, err.Error()
	case errors.Is(err, service.ErrAgentRunStore):
		status, code = 503, err.Error()
	case errors.Is(err, service.ErrAgentToolStore):
		status, code = 503, err.Error()
	case errors.Is(err, service.ErrAgentSessionProfile), errors.Is(err, service.ErrAgentSessionModel):
		status, code = 422, err.Error()
	default:
		if id == "" {
			sessionHTTPError(c, err)
			return
		}
	}
	if id != "" {
		c.Header("Location", "/api/v1/agent/runs/"+id)
	}
	c.JSON(status, gin.H{"code": status, "message": code, "run_id": id})
}

func (h *AgentRunHandler) events(c *gin.Context) {
	values := c.Request.Header.Values("Last-Event-ID")
	if len(values) > 1 || (len(values) == 1 && values[0] == "") {
		runHTTPError(c, service.ErrAgentRunCursorInvalid, "")
		return
	}
	cursor := c.GetHeader("Last-Event-ID")
	// Gin's Flush has no error return. Unwrap to preserve the real network flush
	// result while writes still pass through Gin for status/byte accounting.
	var raw http.ResponseWriter = c.Writer
	for {
		u, ok := raw.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		raw = u.Unwrap()
	}
	controller := http.NewResponseController(raw)
	defer controller.SetWriteDeadline(time.Time{})
	write := func(frame []byte) error {
		defer controller.SetWriteDeadline(time.Time{})
		if err := c.Request.Context().Err(); err != nil {
			return err
		}
		if err := controller.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
			return err
		}
		if _, err := c.Writer.Write(frame); err != nil {
			return err
		}
		return controller.Flush()
	}
	_, err := h.app.Events(c.Request.Context(), agentLocalActor, c.Param("runID"), cursor, agentbridge.StreamCallbacks{
		Ready: func() error {
			c.Header("Content-Type", "text/event-stream; charset=utf-8")
			c.Header("X-Accel-Buffering", "no")
			return write([]byte(": connected\n\n"))
		},
		Keepalive: func() error { return write([]byte(": keepalive\n\n")) },
		Event: func(event agentbridge.Event) error {
			data, err := sonic.Marshal(event)
			if err != nil {
				return err
			}
			return write([]byte("id: " + event.ID + "\nevent: " + event.Type + "\ndata: " + string(data) + "\n\n"))
		},
	})
	// After headers, EOF/errors close the subscription; never invent a terminal
	// event, append JSON to SSE, cancel the Run or reset the consumer's cursor.
	if err != nil && !c.Writer.Written() {
		c.Header("Content-Type", "")
		c.Header("X-Accel-Buffering", "")
		runHTTPError(c, err, "")
	}
}
func (h *AgentRunHandler) send(c *gin.Context) {
	var input service.AgentRunMessage
	if !decodeModelBody(c, &input) {
		return
	}
	value, err := h.app.Send(c.Request.Context(), agentLocalActor, c.Param("sessionID"), input)
	if err != nil {
		runHTTPError(c, err, value.RunID)
		return
	}
	c.Header("Location", value.RunURL)
	c.JSON(202, value)
}
func (h *AgentRunHandler) get(c *gin.Context) {
	value, err := h.app.Get(c.Request.Context(), agentLocalActor, c.Param("runID"))
	if err != nil {
		runHTTPError(c, err, "")
		return
	}
	c.JSON(200, value)
}
func (h *AgentRunHandler) cancel(c *gin.Context) {
	var input struct{}
	if !decodeModelBody(c, &input) {
		return
	}
	value, err := h.app.Cancel(c.Request.Context(), agentLocalActor, c.Param("runID"))
	if err != nil {
		runHTTPError(c, err, "")
		return
	}
	c.JSON(200, value)
}
func (h *AgentRunHandler) reconcile(c *gin.Context) {
	var input struct{}
	if !decodeModelBody(c, &input) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	v, err := h.app.Reconcile(ctx, agentLocalActor, c.Param("runID"))
	if err != nil {
		runHTTPError(c, err, "")
		return
	}
	c.JSON(200, v)
}
func (h *AgentRunHandler) authorization(c *gin.Context) {
	modelHeaders(c)
	if c.Request.URL.RawQuery != "" || c.Request.URL.ForceQuery || c.Request.ContentLength != 0 || len(c.Request.TransferEncoding) != 0 || c.GetHeader("Content-Encoding") != "" {
		runHTTPError(c, service.ErrAgentRunInput, "")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
	defer cancel()
	claims, err := h.app.Authorization(ctx, c.Param("runID"))
	if err != nil {
		c.JSON(403, gin.H{"code": 403, "message": "agent_capability_rejected"})
		return
	}
	body, err := agentrunauth.CanonicalClaims(claims)
	if err != nil {
		c.Status(503)
		return
	}
	c.Data(200, "application/json; charset=utf-8", body)
}
