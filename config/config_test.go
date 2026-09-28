package config

import (
	"os"
	"path/filepath"
	"testing"
)

func configTestFile(t *testing.T, contents string) string {
	t.Helper()
	for _, key := range []string{
		"QUANT4DAD_STORAGE_BACKEND", "QUANT4DAD_DB_DSN",
		"QUANT4DAD_DATASOURCE_PROVIDER", "QUANT4DAD_DATASOURCE_TOKEN",
	} {
		t.Setenv(key, "")
	}
	path := filepath.Join(t.TempDir(), "quant4dad.yaml")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSharedYAMLIgnoresBusinessEnvironment(t *testing.T) {
	path := configTestFile(t, "storage:\n  backend: mysql\n  mysql:\n    dsn: yaml-dsn\ndatasource:\n  provider: tushare\n  token: yaml-token\nsecurity:\n  token: yaml-auth\nweb:\n  api_base_url: http://yaml-api:8080\n")
	for key, value := range map[string]string{"QUANT4DAD_STORAGE_BACKEND": "bad", "QUANT4DAD_DB_DSN": "env-dsn", "QUANT4DAD_AUTH_TOKEN": "env-token", "QUANT4DAD_DATASOURCE_PROVIDER": "env-provider", "QUANT4DAD_DATASOURCE_TOKEN": "env-token", "QUANT4DAD_API_BASE_URL": "http://env-api", "QUANT4DAD_OSS_AK": "env-ak"} {
		t.Setenv(key, value)
	}
	for _, loader := range []func(string) (*Config, error){LoadForApp, LoadForMCP} {
		cfg, err := loader(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Storage.Backend != StorageBackendMySQL || cfg.Storage.MySQL.DSN != "yaml-dsn" || cfg.Security.Token != "yaml-auth" || cfg.Datasource.Provider != "tushare" || cfg.Datasource.Token != "yaml-token" || cfg.Web.APIBaseURL != "http://yaml-api:8080" || cfg.Archive.OSS.AccessKeyID != "" {
			t.Fatal("business ENV overrode shared YAML")
		}
	}
}

func TestStrictCompleteConfiguration(t *testing.T) {
	for _, invalid := range []string{"", "null", "[]", "---", "{}\n---\n{}", "storage:\n  typo: secret", "storage:\n  backend: mysql", "server:\n  addr: ':99999'", "web:\n  api_base_url: http://secret@api", "mcp:\n  external_token: short", "archive:\n  enabled: true", "backtest:\n  worker_pool: 0"} {
		if _, err := Parse([]byte(invalid)); err == nil {
			t.Fatal("accepted invalid configuration")
		}
	}
	cfg, err := Parse([]byte("{}"))
	if err != nil || cfg.Storage.Backend != StorageBackendSQLite || cfg.Server.Addr != ":8080" || cfg.MCP.Addr != ":8080" {
		t.Fatal("defaults unavailable", err)
	}
	if _, err := Load("quant4dad.yaml"); err != nil {
		t.Fatal("checked-in template is invalid", err)
	}
}
