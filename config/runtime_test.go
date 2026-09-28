package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalRuntime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("security:\n  token: local-fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Config.Security.Token != "local-fixture" || r.Config.Datasource.Provider != "" || r.Config.AutoSync.Enabled || r.Config.News.Enabled {
		t.Fatal("local configuration must work without any installed data collector")
	}
	if _, err := Open(context.Background(), path+".missing"); err == nil {
		t.Fatal("missing config accepted")
	}
	if err := os.WriteFile(path, []byte("unknown_secret_field: private-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStartup(path); err == nil {
		t.Fatal("invalid config accepted")
	}
}
