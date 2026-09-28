package domain

import "time"

const (
	ExternalMCPActor      = "external-mcp"
	ExternalMCPProfile    = "external_mcp"
	ExternalMCPSigningKey = "external-mcp"
)

// ExternalMCPSession is a bounded, server-owned MCP execution session. It is
// separate from DSH conversations and never receives a Runtime capability.
type ExternalMCPSession struct {
	ID                string    `json:"id"`
	ExpiresAt         time.Time `json:"expires_at"`
	MaxToolCalls      int64     `json:"max_tool_calls"`
	AuthorizationMode string    `json:"authorization_mode"`
}
