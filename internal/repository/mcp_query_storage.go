package repository

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"

	"github.com/quant4dad/config"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// OpenMCPQueryStorage never creates a SQLite DB or runs migrations. A MySQL
// deployment should additionally grant this process only SELECT permissions.
func OpenMCPQueryStorage(cfg config.StorageConfig) (*gorm.DB, error) {
	if cfg.Backend == config.StorageBackendMySQL {
		return openMySQL(cfg.MySQL)
	}
	if cfg.Backend != config.StorageBackendSQLite && cfg.Backend != config.StorageBackendCSV {
		return nil, errors.New("mcp_storage_invalid")
	}
	path, err := filepath.Abs(cfg.SQLite.Path)
	if err != nil || cfg.SQLite.Path == "" {
		return nil, errors.New("mcp_storage_invalid")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("mcp_storage_not_initialized")
	}
	source := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_query_only=1"}
	return gorm.Open(sqlite.Open(source.String()), defaultGormConfig())
}
func OpenCSVBarQueryRepository(baseDir string) (BarRepository, error) {
	info, err := os.Stat(baseDir)
	if err != nil || !info.IsDir() {
		return nil, errors.New("mcp_csv_not_initialized")
	}
	return &csvBarRepository{baseDir: baseDir}, nil
}
