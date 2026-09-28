//go:build mcpintegration

package mcp

import (
	"bufio"
	"encoding/base64"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/gin-gonic/gin"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/utils/tooljson"
)

func TestInternalMCPControlHost(t *testing.T) {
	if os.Getenv("Q4D_MCP_FIXTURE") != "1" {
		t.Fatal("synthetic integration fixture only")
	}
	f := newGatewayFixture(t, 4)
	router := gin.New()
	router.Any("/internal/mcp", gin.WrapH(f.handler))
	server := httptest.NewServer(router)
	defer server.Close()
	emit := func(value any) {
		data, _ := sonic.Marshal(value)
		_, _ = os.Stdout.Write(append(append([]byte("Q4D_GATEWAY\t"), data...), '\n'))
	}
	emit(map[string]any{"ready": true, "url": server.URL + "/internal/mcp", "capability": f.capability, "claims": f.claims, "catalog": f.app.Definitions(f.claims)})
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 256<<10)
	for scanner.Scan() {
		var input struct {
			ID        int    `json:"id"`
			Op        string `json:"op"`
			Arguments string `json:"arguments"`
		}
		if sonic.Unmarshal(scanner.Bytes(), &input) != nil {
			t.Fatal("invalid fixture control")
		}
		result := map[string]any{"id": input.ID}
		switch input.Op {
		case "status":
			var rows []domain.AgentToolAudit
			if err := f.db.Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			result["queries"] = f.queries.Load()
			result["audits"] = rows
		case "revoke":
			f.ended.Store(true)
		case "change-bars":
			if err := f.db.Model(&domain.Bar{}).Where("code = ?", "sh.600519").Update("close", 999).Error; err != nil {
				t.Fatal(err)
			}
		case "canonical":
			data, err := tooljson.Canonical([]byte(input.Arguments))
			if err != nil {
				result["error"] = "invalid"
			} else {
				hash, _ := tooljson.Hash(data)
				result["canonical_base64"] = base64.StdEncoding.EncodeToString(data)
				result["hash"] = hash
			}
		default:
			t.Fatal("unknown fixture operation")
		}
		emit(result)
	}
	if scanner.Err() != nil {
		t.Fatal("fixture input failed")
	}
}
