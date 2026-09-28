package handler

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/exporter"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
)

type PipelineHandler struct {
	svc *service.PipelineService
	app *application.PipelineApp
}

func NewPipelineHandler(svc *service.PipelineService, app *application.PipelineApp) *PipelineHandler {
	return &PipelineHandler{svc: svc, app: app}
}

func (h *PipelineHandler) Register(rg *gin.RouterGroup) {
	g := rg.Group("/pipelines")
	g.GET("", h.list)
	g.POST("", h.create)
	g.POST("/preview", h.preview)
	g.GET("/:id", h.get)
	g.PUT("/:id", h.update)
	g.PATCH("/:id/status", h.setStatus)
	g.DELETE("/:id", h.delete)
	g.POST("/:id/dry-run", h.dryRun)

	// Node type metadata, for the UI canvas to render config forms.
	rg.GET("/pipeline/node-types", h.nodeTypes)

	// Event ingestion and queries.
	rg.POST("/events/ingest/:pipelineId", h.ingest)
	rg.GET("/events", h.listEvents)
	rg.GET("/events/export", h.exportEvents)
	rg.GET("/events/:id", h.getEvent)
	rg.GET("/events/:id/ai-results", h.eventAIResults)
}

func (h *PipelineHandler) nodeTypes(c *gin.Context) {
	ok(c, h.svc.NodeTypes())
}

func (h *PipelineHandler) list(c *gin.Context) {
	items, err := h.svc.List(c.Request.Context())
	if err != nil {
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, items)
}

func (h *PipelineHandler) create(c *gin.Context) {
	var in service.PipelineInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: err.Error()})
		return
	}
	p, err := h.svc.Create(c.Request.Context(), in)
	if err != nil {
		logger.L().Error("pipeline create failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	created(c, p)
}

func (h *PipelineHandler) update(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	var in service.PipelineInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: err.Error()})
		return
	}
	p, err := h.svc.Update(c.Request.Context(), id, in)
	if err != nil {
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, p)
}

func (h *PipelineHandler) get(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	p, err := h.svc.GetByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, p)
}

type setStatusRequest struct {
	Status          string `json:"status"`
	ExpectedVersion *int   `json:"expected_version,omitempty"`
}

func (h *PipelineHandler) setStatus(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req setStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: err.Error()})
		return
	}
	if err := h.svc.SetStatus(c.Request.Context(), id, req.Status, req.ExpectedVersion); err != nil {
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, gin.H{"ok": true})
}

func (h *PipelineHandler) delete(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		c.JSON(toHTTPError(err))
		return
	}
	c.Status(http.StatusNoContent)
}

type dryRunRequest struct {
	SampleEvent map[string]any `json:"sample_event"`
}

func (h *PipelineHandler) preview(c *gin.Context) {
	var req struct {
		Pipeline    service.PipelineInput `json:"pipeline"`
		SampleEvent map[string]any        `json:"sample_event"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256*1024)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: "pipeline_preview_input_invalid"})
		return
	}
	res, err := h.svc.Preview(c.Request.Context(), req.Pipeline, req.SampleEvent)
	if err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: "pipeline_preview_invalid"})
		return
	}
	ok(c, gin.H{"status": res.Status, "dropped_at_node": res.DroppedAtNode, "failed_at_node": res.FailedAtNode, "traces": res.Traces, "final_payload": res.FinalPayload})
}

func (h *PipelineHandler) dryRun(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req dryRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: err.Error()})
		return
	}
	res, err := h.svc.DryRun(c.Request.Context(), id, req.SampleEvent)
	if err != nil {
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, gin.H{
		"status":          res.Status,
		"dropped_at_node": res.DroppedAtNode,
		"failed_at_node":  res.FailedAtNode,
		"traces":          res.Traces,
		"final_payload":   res.FinalPayload,
	})
}

func (h *PipelineHandler) ingest(c *gin.Context) {
	pipelineID, err := strconv.ParseInt(c.Param("pipelineId"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: "invalid pipelineId"})
		return
	}
	var payload map[string]any
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: "invalid event payload: " + err.Error()})
		return
	}
	source := c.DefaultQuery("source", "webhook")
	evt, err := h.app.Ingest(c.Request.Context(), pipelineID, source, payload)
	if err != nil {
		if errors.Is(err, application.ErrPipelineDisabled) {
			c.JSON(http.StatusConflict, ErrorResponse{Code: 40901, Message: "pipeline is not enabled"})
			return
		}
		logger.L().Error("event ingest failed", zap.Int64("pipeline_id", pipelineID), zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, gin.H{
		"event_id":        evt.ID,
		"event_uid":       evt.EventUID,
		"status":          evt.Status,
		"dropped_at_node": evt.DroppedAtNode,
		"final_payload":   evt.FinalPayload,
	})
}

// parseEventFilter parses the filter shared by the event list and the export: pipeline,
// status, and received-time range. start and end accept RFC3339 or YYYY-MM-DD; a bare date
// for end is treated as covering that whole day, by adding one day.
func parseEventFilter(c *gin.Context) repository.EventQuery {
	q := repository.EventQuery{Status: c.Query("status")}
	if v := c.Query("pipeline_id"); v != "" {
		q.PipelineID, _ = strconv.ParseInt(v, 10, 64)
	}
	if t, ok := parseQueryTime(c.Query("start"), false); ok {
		q.Start = t
	}
	if t, ok := parseQueryTime(c.Query("end"), true); ok {
		q.End = t
	}
	return q
}

// parseQueryTime parses a time parameter. With dayEnd=true and a bare date, it returns the
// next day's midnight, so the half-open [Start, End) interval covers the whole end day. A
// parse failure returns the zero value and false.
func parseQueryTime(s string, dayEnd bool) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		if dayEnd {
			t = t.AddDate(0, 0, 1)
		}
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func (h *PipelineHandler) listEvents(c *gin.Context) {
	q := parseEventFilter(c)
	if v := c.Query("limit"); v != "" {
		q.Limit, _ = strconv.Atoi(v)
	}
	if v := c.Query("offset"); v != "" {
		q.Offset, _ = strconv.Atoi(v)
	}
	items, total, err := h.app.ListEvents(c.Request.Context(), q)
	if err != nil {
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, gin.H{"total": total, "items": items})
}

// eventExportHeaders is the export's header row, in the same order as eventExportRow.
var eventExportHeaders = []string{
	"ID", "事件UID", "流水线", "来源", "状态", "丢弃节点", "错误",
	"接收时间", "完成时间", "原始Payload", "最终Payload",
}

// eventStatusLabel maps the status enum to its display label, matching what the UI shows.
func eventStatusLabel(s string) string {
	switch s {
	case domain.EventStatusProcessing:
		return "处理中"
	case domain.EventStatusPassed:
		return "通过"
	case domain.EventStatusDropped:
		return "已丢弃"
	case domain.EventStatusFailed:
		return "失败"
	default:
		return s
	}
}

func eventExportRow(e *domain.Event, pipelineNames map[int64]string) []string {
	pipeline := pipelineNames[e.PipelineID]
	if pipeline == "" {
		pipeline = "#" + strconv.FormatInt(e.PipelineID, 10)
	}
	finished := ""
	if e.FinishedAt != nil {
		finished = e.FinishedAt.Format("2006-01-02 15:04:05")
	}
	return []string{
		strconv.FormatInt(e.ID, 10),
		e.EventUID,
		pipeline,
		e.Source,
		eventStatusLabel(e.Status),
		e.DroppedAtNode,
		e.Error,
		e.ReceivedAt.Format("2006-01-02 15:04:05"),
		finished,
		string(e.RawPayload),
		string(e.FinalPayload),
	}
}

// exportEvents exports the matching events as CSV or XLSX. format is csv, excel or xlsx,
// defaulting to csv.
func (h *PipelineHandler) exportEvents(c *gin.Context) {
	q := parseEventFilter(c)
	events, pipelineNames, err := h.app.ExportEvents(c.Request.Context(), q)
	if err != nil {
		logger.L().Error("export events failed", zap.Error(err))
		c.JSON(toHTTPError(err))
		return
	}

	rows := make([][]string, 0, len(events))
	for _, e := range events {
		rows = append(rows, eventExportRow(e, pipelineNames))
	}

	format := c.DefaultQuery("format", "csv")
	stamp := time.Now().Format("20060102_150405")

	var (
		data        []byte
		contentType string
		filename    string
	)
	switch format {
	case "excel", "xlsx":
		data, err = exporter.XLSX("events", eventExportHeaders, rows)
		contentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
		filename = "events_" + stamp + ".xlsx"
	default:
		data, err = exporter.CSV(eventExportHeaders, rows)
		contentType = "text/csv; charset=utf-8"
		filename = "events_" + stamp + ".csv"
	}
	if err != nil {
		logger.L().Error("render export failed", zap.String("format", format), zap.Error(err))
		c.JSON(http.StatusInternalServerError, ErrorResponse{Code: 500, Message: "internal server error"})
		return
	}

	// filename* is RFC 5987 encoded to support non-ASCII filenames. This one is pure ASCII,
	// but the form is kept for correctness.
	c.Header("Content-Disposition", "attachment; filename=\""+filename+"\"; filename*=UTF-8''"+url.PathEscape(filename))
	c.Data(http.StatusOK, contentType, data)
}

func (h *PipelineHandler) getEvent(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	evt, err := h.app.GetEvent(c.Request.Context(), id)
	if err != nil {
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, evt)
}

func (h *PipelineHandler) eventAIResults(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	items, err := h.app.ListAIResults(c.Request.Context(), id)
	if err != nil {
		c.JSON(toHTTPError(err))
		return
	}
	ok(c, items)
}
