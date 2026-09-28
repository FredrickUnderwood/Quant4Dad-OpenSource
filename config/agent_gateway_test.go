package config

import "testing"

func TestAgentGatewayConfigurationFailsClosed(t *testing.T) {
	valid := runsConfigFixture()
	valid.Agent.Gateway = AgentGatewayConfig{Enabled: true, ResultDirectory: "/private/results"}
	if valid.ValidateAgentGateway() != nil {
		t.Fatal("complete configuration rejected")
	}
	for _, cfg := range []*Config{nil, {}, {Agent: AgentConfig{Gateway: AgentGatewayConfig{Enabled: true, ResultDirectory: "relative"}}}, {Agent: AgentConfig{Gateway: AgentGatewayConfig{Enabled: true, ResultDirectory: "/private/results"}}}} {
		if cfg.ValidateAgentGateway() == nil {
			t.Fatal("incomplete gateway configuration accepted")
		}
	}
}
