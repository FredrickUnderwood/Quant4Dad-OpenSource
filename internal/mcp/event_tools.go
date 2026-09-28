package mcp

import (
	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/service"
)

func RegisterEventTools(s *Server, svc *service.EventQueryService) {
	for _, tool := range application.EventToolDefinitions(svc) {
		s.Register(tool)
	}
}
