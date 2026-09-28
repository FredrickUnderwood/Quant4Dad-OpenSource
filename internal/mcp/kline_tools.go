package mcp

import (
	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/service"
)

func RegisterKlineTools(s *Server, svc *service.InstrumentService) {
	for _, tool := range application.MarketToolDefinitions(svc) {
		s.Register(tool)
	}
}
