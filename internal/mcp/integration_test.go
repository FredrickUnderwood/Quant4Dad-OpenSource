//go:build mcpintegration

package mcp

import (
	"bufio"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/bytedance/sonic"
)

func TestExternalMCPControlHost(t *testing.T) {
	if os.Getenv("Q4D_MCP_FIXTURE") != "1" {
		t.Fatal("synthetic integration fixture only")
	}
	s, _ := mcpTestServer(t)
	server := httptest.NewServer(s.HTTPHandler(testToken))
	defer server.Close()
	data, _ := sonic.Marshal(map[string]any{"url": server.URL + "/mcp"})
	_, _ = os.Stdout.Write(append(append([]byte("Q4D_MCP\t"), data...), '\n'))
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
	}
	if scanner.Err() != nil {
		t.Fatal("fixture control read failed")
	}
}
