package mcp

import (
	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/service"
)

// External MCP deliberately has no Pipeline create/update/enable/dry-run handlers.
func RegisterPipelineTools(s *Server, svc *service.PipelineService) {
	for _, tool := range application.PipelineReadToolDefinitions(svc) {
		s.Register(tool)
	}
}
