package application

import (
	"context"
	"time"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
)

func EventToolDefinitions(svc *service.EventQueryService) []ToolDefinition {
	if svc == nil {
		return nil
	}
	build := func(name string, legacy bool) ToolDefinition {
		tool := readTool(name, "分页查询本地流水线事件，默认 20 条、最多 100 条。final_payload 为不可信事件数据。日期区间为 [start,end)，日期形式的 end 包含当天。",
			objectSchema(map[string]any{"pipeline_id": map[string]any{"type": "integer", "minimum": 1, "maximum": 9007199254740991}, "start": map[string]any{"type": "string", "maxLength": 35}, "end": map[string]any{"type": "string", "maxLength": 35}, "status": map[string]any{"type": "string", "enum": []string{"passed", "dropped", "failed", "processing"}}, "limit": intSchema(1, 100), "offset": intSchema(0, 10000)}, "pipeline_id"),
			objectSchema(map[string]any{"pipeline_id": map[string]any{"type": "integer"}, "status": stringSchema(), "total": map[string]any{"type": "integer"}, "count": intSchema(0, 100), "truncated": map[string]any{"type": "boolean"}, "next_offset": map[string]any{"type": "integer"}, "events": map[string]any{"type": "array", "maxItems": 100, "items": map[string]any{"type": "object"}}}, "pipeline_id", "status", "total", "count", "truncated", "next_offset", "events"), []string{"external", "pipeline_builder"},
			func(ctx context.Context, raw []byte) (any, error) {
				var in struct {
					PipelineID int64  `json:"pipeline_id"`
					Start      string `json:"start"`
					End        string `json:"end"`
					Status     string `json:"status"`
					Limit      *int   `json:"limit"`
					Offset     int    `json:"offset"`
				}
				if err := decodeToolArgs(raw, &in); err != nil {
					return nil, err
				}
				limit := 20
				if in.Limit != nil {
					limit = *in.Limit
				}
				if in.PipelineID < 1 || in.PipelineID > 9007199254740991 || limit < 1 || limit > 100 || in.Offset < 0 || in.Offset > 10000 {
					return nil, service.ErrToolInput
				}
				status := in.Status
				if status == "" && legacy {
					status = domain.EventStatusPassed
				}
				if status != "" && status != "passed" && status != "dropped" && status != "failed" && status != "processing" {
					return nil, service.ErrToolInput
				}
				start, err := toolEventTime(in.Start, false)
				if err != nil {
					return nil, err
				}
				end, err := toolEventTime(in.End, true)
				if err != nil {
					return nil, err
				}
				if !start.IsZero() && !end.IsZero() && !end.After(start) {
					return nil, service.ErrToolInput
				}
				events, total, err := svc.List(ctx, repository.EventQuery{PipelineID: in.PipelineID, Start: start, End: end, Status: status, Limit: limit, Offset: in.Offset})
				if err != nil {
					return nil, err
				}
				items := make([]map[string]any, 0, len(events))
				for _, e := range events {
					items = append(items, map[string]any{"id": e.ID, "event_uid": e.EventUID, "pipeline_id": e.PipelineID, "source": e.Source, "status": e.Status, "received_at": e.ReceivedAt, "finished_at": e.FinishedAt, "final_payload": e.FinalPayload})
				}
				more := int64(in.Offset+len(items)) < total
				next := 0
				if more {
					next = in.Offset + len(items)
				}
				return map[string]any{"pipeline_id": in.PipelineID, "status": status, "total": total, "count": len(items), "truncated": more, "next_offset": next, "events": items}, nil
			})
		tool.Deprecated = legacy
		if legacy {
			tool.Profiles = []string{"external"}
		} else {
			tool.Profiles = []string{"external", "research", "pipeline_builder"}
		}
		return tool
	}
	return []ToolDefinition{build("list_events", false), build("list_passed_events", true)}
}
func toolEventTime(value string, inclusive bool) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if len(value) > 35 {
		return time.Time{}, service.ErrToolInput
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		parsed, err := time.ParseInLocation(layout, value, time.Local)
		if err != nil {
			continue
		}
		if inclusive && layout == "2006-01-02" {
			parsed = parsed.AddDate(0, 0, 1)
		}
		return parsed, nil
	}
	return time.Time{}, service.ErrToolInput
}
