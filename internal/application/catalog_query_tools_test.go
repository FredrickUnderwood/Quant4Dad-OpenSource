package application

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
)

func TestCatalogQueryToolsBoundedProjectionsAndValidation(t *testing.T) {
	db, err := repository.Open(config.StorageConfig{Backend: config.StorageBackendSQLite, SQLite: config.SQLiteConfig{Path: filepath.Join(t.TempDir(), "catalog.db")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	if err := repository.Migrate(db, config.StorageBackendSQLite); err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 22; i++ {
		st := domain.Strategy{ID: i, Name: strings.Repeat("x", int(i)), Description: strings.Repeat("d", 3000), Body: []byte(`{"private_large_body":true}`)}
		if err := db.Create(&st).Error; err != nil {
			t.Fatal(err)
		}
	}
	news := domain.News{Source: "synthetic", ExternalID: "1", Content: strings.Repeat("中", 10000), Raw: []byte(`{"secret":"must_not_leave_storage"}`)}
	if err := db.Create(&news).Error; err != nil {
		t.Fatal(err)
	}
	catalog := NewToolCatalogApplication()
	for _, d := range CatalogQueryToolDefinitions(service.NewAgentCatalogQueryService(repository.NewAgentCatalogQueryRepository(db))) {
		catalog.Register(d)
	}
	for _, d := range CatalogValidationToolDefinitions(service.NewStrategyService(nil), nil, nil, service.NewIndicatorService()) {
		catalog.Register(d)
	}
	call := func(profile, name, args string) []byte {
		t.Helper()
		body, err := catalog.Invoke(context.Background(), profile, name, []byte(args))
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	first := call("strategy_lab", "list_strategies", `{}`)
	if strings.Contains(string(first), "private_large_body") || len(first) > 20000 {
		t.Fatal("full strategy list escaped")
	}
	var page struct {
		Data struct {
			Count int  `json:"count"`
			More  bool `json:"has_more"`
			Next  int  `json:"next_before_id"`
		}
	}
	if sonic.Unmarshal(first, &page) != nil || page.Data.Count != 20 || !page.Data.More || page.Data.Next != 3 {
		t.Fatalf("first page: %s", first)
	}
	last := call("strategy_lab", "list_strategies", `{"before_id":3}`)
	if sonic.Unmarshal(last, &page) != nil || page.Data.Count != 2 || page.Data.More || page.Data.Next != 0 {
		t.Fatalf("last page: %s", last)
	}
	result := call("research", "get_news", `{"id":1}`)
	if strings.Contains(string(result), "secret") || strings.Contains(string(result), strings.Repeat("中", 8193)) {
		t.Fatal("news raw/unbounded content escaped")
	}
	valid := `{"strategy":{"name":"test","universe":["sh.600519"],"period":"1d","body":{"indicators":[],"rules":[],"execution":{"fill_at":"close"}}}}`
	_ = call("strategy_lab", "validate_strategy", valid)
	_ = call("strategy_lab", "validate_strategy", strings.Replace(valid, `"indicators":[]`, `"indicators":[{"type":"MA","alias":"average","params":{"period":20}}]`, 1))
	if _, err := catalog.Invoke(context.Background(), "strategy_lab", "validate_strategy", []byte(strings.Replace(valid, `"name":"test"`, `"name":null`, 1))); err == nil {
		t.Fatal("schema admitted null for a required string")
	}
	for _, raw := range []string{strings.Replace(valid, `"fill_at":"close"`, `"fill_at":"invalid"`, 1), strings.Replace(valid, `"indicators":[]`, `"mode":"script","lang":"python","code":"unsupported","indicators":[]`, 1), strings.Replace(valid, `"sh.600519"`, `"../../secret"`, 1)} {
		if _, err := catalog.Invoke(context.Background(), "strategy_lab", "validate_strategy", []byte(raw)); err == nil {
			t.Fatal("invalid strategy admitted")
		}
	}
	if _, err := catalog.Invoke(context.Background(), "external", "get_strategy", []byte(`{"id":1}`)); err == nil {
		t.Fatal("unapproved external scope")
	}
}

// Publication times intentionally disagree with insertion time and ID order.
// This verifies what the news tools expose, not whether a model follows prose.
func TestNewsToolsPublicationTimeAndKeysetPages(t *testing.T) {
	db, err := repository.Open(config.StorageConfig{Backend: config.StorageBackendSQLite, SQLite: config.SQLiteConfig{Path: filepath.Join(t.TempDir(), "news.db")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	if err := db.AutoMigrate(&domain.News{}); err != nil {
		t.Fatal(err)
	}
	inserted := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	for i, day := range []int{3, 4, 2, 5, 1} {
		row := domain.News{ID: int64((i + 1) * 10), Source: "fixture", ExternalID: time.Date(2025, 1, day, 0, 0, 0, 0, time.UTC).Format(time.RFC3339), PublishedAt: time.Date(2025, 1, day, 0, 0, 0, 0, time.UTC), CreatedAt: inserted}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	catalog := NewToolCatalogApplication()
	for _, d := range CatalogQueryToolDefinitions(service.NewAgentCatalogQueryService(repository.NewAgentCatalogQueryRepository(db))) {
		catalog.Register(d)
	}
	invoke := func(name, args string) []byte {
		t.Helper()
		body, err := catalog.Invoke(context.Background(), "research", name, []byte(args))
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	type newsItem struct {
		ID          int64     `json:"id"`
		PublishedAt time.Time `json:"published_at"`
	}
	var page struct {
		Data struct {
			Items []newsItem `json:"items"`
			Count int        `json:"count"`
			More  bool       `json:"has_more"`
			Next  int64      `json:"next_before_id"`
		} `json:"data"`
	}
	first := invoke("list_news", `{"limit":2}`)
	if err := sonic.Unmarshal(first, &page); err != nil {
		t.Fatal(err)
	}
	if page.Data.Count != 2 || len(page.Data.Items) != 2 || page.Data.Items[0].ID != 50 || page.Data.Items[1].ID != 40 || !page.Data.More || page.Data.Next != 40 {
		t.Fatalf("first page: %s", first)
	}
	if !page.Data.Items[0].PublishedAt.Before(page.Data.Items[1].PublishedAt) || page.Data.Items[0].PublishedAt.Equal(inserted) {
		t.Fatalf("not publication time in ID order: %s", first)
	}
	published := page.Data.Items[0].PublishedAt
	second := invoke("list_news", `{"limit":2,"before_id":40}`)
	if err := sonic.Unmarshal(second, &page); err != nil {
		t.Fatal(err)
	}
	if page.Data.Count != 2 || len(page.Data.Items) != 2 || page.Data.Items[0].ID != 30 || page.Data.Items[1].ID != 20 || !page.Data.More || page.Data.Next != 20 {
		t.Fatalf("second page: %s", second)
	}
	last := invoke("list_news", `{"limit":2,"before_id":20}`)
	if err := sonic.Unmarshal(last, &page); err != nil {
		t.Fatal(err)
	}
	if page.Data.Count != 1 || len(page.Data.Items) != 1 || page.Data.Items[0].ID != 10 || page.Data.More || page.Data.Next != 0 {
		t.Fatalf("last page: %s", last)
	}
	detail := invoke("get_news", `{"id":50}`)
	var item struct {
		Data newsItem `json:"data"`
	}
	if err := sonic.Unmarshal(detail, &item); err != nil {
		t.Fatal(err)
	}
	if item.Data.ID != 50 || !item.Data.PublishedAt.Equal(published) {
		t.Fatalf("detail differs from list: %s", detail)
	}
	for _, body := range [][]byte{first, second, last, detail} {
		if strings.Contains(string(body), `"created_at"`) || strings.Contains(string(body), inserted.Format(time.RFC3339)) {
			t.Fatalf("insertion time leaked: %s", body)
		}
	}
}
