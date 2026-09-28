package repository

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
)

type preETFInstrument struct {
	Code       string `gorm:"primaryKey;size:32"`
	Name       string `gorm:"size:64;not null"`
	Industry   string `gorm:"size:64"`
	ListedDate time.Time
	Status     string `gorm:"size:16;default:active"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (preETFInstrument) TableName() string { return "instrument" }

func TestETFMigrationPreservesStocksAndMetadataUpserts(t *testing.T) {
	db, err := Open(config.StorageConfig{Backend: config.StorageBackendSQLite, SQLite: config.SQLiteConfig{Path: filepath.Join(t.TempDir(), "etf.db")}})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	models := []any{&preETFInstrument{}}
	for _, model := range schemaModels(config.StorageBackendSQLite) {
		if _, instrument := model.(*domain.Instrument); !instrument {
			models = append(models, model)
		}
	}
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&preETFInstrument{Code: "sh.600519", Name: "贵州茅台", Status: "active"}).Error; err != nil {
		t.Fatal(err)
	}
	if ok, err := CheckReleaseCompatibility(db, config.StorageBackendSQLite); err != nil || !ok {
		t.Fatalf("additive ETF migration rejected: %v %v", ok, err)
	}
	if ok, err := CheckSchema(db, config.StorageBackendSQLite); err != nil || ok {
		t.Fatalf("unmigrated runtime should fail schema check: %v %v", ok, err)
	}
	if err := Migrate(db, config.StorageBackendSQLite); err != nil {
		t.Fatal(err)
	}
	r := NewInstrumentRepository(db)
	stock, err := r.GetByCode(context.Background(), "sh.600519")
	if err != nil || stock.AssetType != domain.AssetStock {
		t.Fatalf("existing stock not backfilled with asset type: %+v %v", stock, err)
	}
	etf := &domain.Instrument{Code: "sh.510300", Name: "沪深300ETF", AssetType: domain.AssetETF, Status: "active", IndexCode: "000300.SH", Manager: "旧管理人"}
	if err := r.Upsert(context.Background(), []*domain.Instrument{etf}); err != nil {
		t.Fatal(err)
	}
	etf.Manager = "新管理人"
	etf.Status = "delisted"
	if err := r.Upsert(context.Background(), []*domain.Instrument{etf}); err != nil {
		t.Fatal(err)
	}
	items, total, err := r.List(context.Background(), ListInstrumentsFilter{AssetType: domain.AssetETF})
	if err != nil || total != 1 || len(items) != 1 || items[0].Manager != "新管理人" || items[0].Status != "delisted" || items[0].ListedDate != nil {
		t.Fatalf("ETF metadata refresh/filter failed: %+v %d %v", items, total, err)
	}
}
