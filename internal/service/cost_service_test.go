package service

import (
	"context"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func TestCostServiceRejectsInvalidValuesBeforeWriting(t *testing.T) {
	db, err := repository.Open(config.StorageConfig{Backend: config.StorageBackendSQLite, SQLite: config.SQLiteConfig{Path: filepath.Join(t.TempDir(), "cost.db")}})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&domain.Cost{}); err != nil {
		t.Fatal(err)
	}
	svc := NewCostService(repository.NewCostRepository(db))
	ctx := context.Background()
	original := domain.Cost{Name: "Synthetic cost", CommissionRate: .0003, MinCommission: 5, StampDutyRate: .0005, SlippageBps: 5}
	if err := svc.Create(ctx, &original); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(*domain.Cost)
	}{
		{"empty name", func(c *domain.Cost) { c.Name = "  " }}, {"long name", func(c *domain.Cost) { c.Name = strings.Repeat("测", 65) }},
		{"control", func(c *domain.Cost) { c.Name = "a\nb" }}, {"invalid utf8", func(c *domain.Cost) { c.Name = string([]byte{0xff}) }},
		{"negative commission", func(c *domain.Cost) { c.CommissionRate = -1 }}, {"negative minimum", func(c *domain.Cost) { c.MinCommission = -1 }},
		{"nan stamp", func(c *domain.Cost) { c.StampDutyRate = math.NaN() }}, {"infinite slippage", func(c *domain.Cost) { c.SlippageBps = math.Inf(1) }},
		{"infinite minimum", func(c *domain.Cost) { c.MinCommission = math.Inf(-1) }}, {"excess commission", func(c *domain.Cost) { c.CommissionRate = 1.01 }},
		{"excess stamp", func(c *domain.Cost) { c.StampDutyRate = 1.01 }}, {"excess slippage", func(c *domain.Cost) { c.SlippageBps = 10001 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			create := original
			create.ID = 0
			create.Name = "New synthetic cost"
			tc.change(&create)
			if err := svc.Create(ctx, &create); err == nil {
				t.Fatal("invalid create succeeded")
			}
			update := original
			tc.change(&update)
			if err := svc.Update(ctx, &update); err == nil {
				t.Fatal("invalid update succeeded")
			}
			stored, err := svc.GetByID(ctx, original.ID)
			if err != nil || stored.Name != original.Name || stored.CommissionRate != original.CommissionRate || stored.MinCommission != original.MinCommission || stored.StampDutyRate != original.StampDutyRate || stored.SlippageBps != original.SlippageBps {
				t.Fatalf("rejected update changed row: %+v %v", stored, err)
			}
			var count int64
			if err := db.Model(&domain.Cost{}).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("rejected create changed row count: %d %v", count, err)
			}
		})
	}
	valid := original
	valid.Name = "  Zero cost  "
	valid.CommissionRate = 0
	valid.MinCommission = 0
	valid.StampDutyRate = 0
	valid.SlippageBps = 0
	if err := svc.Update(ctx, &valid); err != nil {
		t.Fatal(err)
	}
	stored, err := svc.GetByID(ctx, original.ID)
	if err != nil || stored.Name != "Zero cost" || stored.CommissionRate != 0 || stored.MinCommission != 0 || stored.StampDutyRate != 0 || stored.SlippageBps != 0 {
		t.Fatalf("valid zero values not saved: %+v %v", stored, err)
	}
	if err := svc.Create(ctx, nil); err == nil {
		t.Fatal("nil model accepted")
	}
}
