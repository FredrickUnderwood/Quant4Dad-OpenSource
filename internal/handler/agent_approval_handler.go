package handler

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/service"
	"regexp"
	"time"
)

type AgentApprovalHandler struct {
	app *application.AgentApprovalApplication
}

func NewAgentApprovalHandler(app *application.AgentApprovalApplication) *AgentApprovalHandler {
	return &AgentApprovalHandler{app}
}

var approvalIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (h *AgentApprovalHandler) Register(rg *gin.RouterGroup) {
	g := rg.Group("/agent/approvals")
	g.Use(func(c *gin.Context) {
		modelHeaders(c)
		if !approvalIDPattern.MatchString(c.Param("approvalID")) || !sessionNoQuery(c) || c.Request.Header.Get("Content-Encoding") != "" {
			if !c.Writer.Written() {
				c.JSON(400, gin.H{"code": 400, "message": "agent_approval_invalid"})
			}
			c.Abort()
		}
	})
	g.GET("/:approvalID", func(c *gin.Context) {
		if c.Request.ContentLength != 0 || len(c.Request.TransferEncoding) > 0 {
			c.JSON(400, gin.H{"code": 400, "message": "agent_approval_invalid"})
			return
		}
		row, err := h.app.Get(c.Request.Context(), agentLocalActor, c.Param("approvalID"))
		if err != nil {
			c.JSON(404, gin.H{"code": 404, "message": "agent_approval_missing"})
			return
		}
		c.JSON(200, row)
	})
	g.POST("/:approvalID/decision", func(c *gin.Context) {
		var in struct {
			Decision string `json:"decision"`
		}
		if !decodeModelBody(c, &in) {
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()
		result, err := h.app.Decide(ctx, agentLocalActor, c.Param("approvalID"), in.Decision)
		if err != nil {
			status := 503
			code := "agent_approval_delivery_unconfirmed"
			if errors.Is(err, service.ErrAgentApproval) {
				status = 409
				code = "agent_approval_rejected"
			}
			c.JSON(status, gin.H{"code": status, "message": code, "approval": result.Approval, "delivered": false})
			return
		}
		c.JSON(200, result)
	})
}
