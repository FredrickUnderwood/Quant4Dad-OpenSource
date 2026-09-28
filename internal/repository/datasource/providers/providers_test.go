package providers_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository/datasource"
	_ "github.com/quant4dad/internal/repository/datasource/providers"
)

// Use the API's registration and configuration loading path. An unextended
// checkout must start without making network calls or silently mounting feeds.
func TestStartupWithoutBundledProviders(t *testing.T) {
	if got := datasource.Providers(); len(got) != 0 {
		t.Fatalf("unexpected providers: %v", got)
	}
	if got := datasource.NewsSourceNames(); len(got) != 0 {
		t.Fatalf("unexpected news sources: %v", got)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadForApp(path)
	if err != nil {
		t.Fatal(err)
	}
	client, err := datasource.New(cfg.Datasource)
	if err != nil || !datasource.IsUnavailable(client) {
		t.Fatalf("unexpected client: %v %v", client, err)
	}
	if _, err := client.ListInstruments(context.Background()); !errors.Is(err, datasource.ErrNoProvider) {
		t.Fatalf("catalog: %v", err)
	}
	if _, err := client.FetchBars(context.Background(), "sh.600000", domain.Bar1d, time.Time{}, time.Time{}); !errors.Is(err, datasource.ErrNoProvider) {
		t.Fatalf("bars: %v", err)
	}
}
