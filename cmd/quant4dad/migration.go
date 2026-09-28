package main

import (
	"context"
	"os"
	"time"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository"
)

// check=0 means ready, 10 means migrations are required, 1 means failure.
// apply/compatible use 0/1. All modes read the local configuration.
func runMigration(local, mode string) int {
	if mode != "check" && mode != "apply" && mode != "compatible" {
		return 1
	}
	cfg, err := config.LoadStartup(local)
	if err != nil {
		return 1
	}
	if err := logger.Init(logger.Config{Level: "error"}); err != nil {
		return 1
	}
	defer logger.Shutdown()
	openStorage := repository.Open
	if mode != "apply" && cfg.Storage.Backend != config.StorageBackendMySQL {
		if _, err := os.Stat(cfg.Storage.SQLite.Path); os.IsNotExist(err) {
			if mode == "check" {
				return 10
			}
			return 1
		}
		openStorage = repository.OpenMCPQueryStorage
	}
	db, err := openStorage(cfg.Storage)
	if err != nil {
		return 1
	}
	sqlDB, err := db.DB()
	if err != nil {
		return 1
	}
	defer sqlDB.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db = db.WithContext(ctx)
	if mode == "apply" {
		if err := repository.Migrate(db, cfg.Storage.Backend); err != nil {
			return 1
		}
	}
	checkSchema := repository.CheckSchema
	if mode == "compatible" {
		checkSchema = repository.CheckReleaseCompatibility
	}
	ready, err := checkSchema(db, cfg.Storage.Backend)
	if err != nil {
		return 1
	}
	if !ready {
		if mode == "check" {
			return 10
		}
		return 1
	}
	// Compatibility allows only the explicitly declared additive title columns.
	// Check/apply/startup still require the entire current schema.
	return 0
}
