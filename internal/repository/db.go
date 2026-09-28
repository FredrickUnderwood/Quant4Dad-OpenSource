package repository

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
)

// Open returns the metadata GORM DB. When backend=csv the metadata still lives
// in the sqlite store (csv only redirects bar data).
func Open(cfg config.StorageConfig) (*gorm.DB, error) {
	switch cfg.Backend {
	case config.StorageBackendMySQL:
		return openMySQL(cfg.MySQL)
	case config.StorageBackendSQLite, config.StorageBackendCSV:
		return openSQLite(cfg.SQLite)
	default:
		return nil, errors.New("repository.Open: invalid backend")
	}
}

func openSQLite(cfg config.SQLiteConfig) (*gorm.DB, error) {
	if cfg.Path == "" {
		return nil, errors.New("sqlite.path is empty")
	}
	if dir := filepath.Dir(cfg.Path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	return gorm.Open(sqlite.Open(cfg.Path), defaultGormConfig())
}

func openMySQL(cfg config.MySQLConfig) (*gorm.DB, error) {
	db, err := gorm.Open(mysql.Open(cfg.DSN), defaultGormConfig())
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	if cfg.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime > 0 {
		sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	} else {
		sqlDB.SetConnMaxLifetime(time.Hour)
	}
	return db, nil
}

func defaultGormConfig() *gorm.Config {
	return &gorm.Config{
		NamingStrategy: schema.NamingStrategy{SingularTable: true},
		Logger:         logger.GORM(),
	}
}

// Migrate ensures all metadata tables exist. The Bar table is included only
// when the bar storage backend is sqlite/mysql; csv mode skips it.
func Migrate(db *gorm.DB, backend string) error {
	return db.AutoMigrate(schemaModels(backend)...)
}

func schemaModels(backend string) []any {
	models := []any{
		&domain.Instrument{},
		&domain.Strategy{},
		&domain.Cost{},
		&domain.BacktestJob{},
		&domain.BacktestResult{},
		&domain.Trade{},
		&domain.EquityPoint{},
		&domain.DataSyncTask{},
		&domain.DataSyncFailure{},
		&domain.DataCoverageSummary{},
		&domain.Pipeline{},
		&domain.PipelineNode{},
		&domain.PipelineEdge{},
		&domain.Event{},
		&domain.EventTrace{},
		&domain.AIResult{},
		&domain.Setting{},
		&domain.AgentSessionBinding{},
		&domain.AgentRequestBinding{},
		&domain.AgentToolAudit{},
		&domain.AgentApprovalReceipt{},
		&domain.AgentToolEffect{},
		&domain.News{},
		&domain.NewsSubscription{},
	}
	if backend != config.StorageBackendCSV {
		models = append(models, &domain.Bar{})
	}
	return models
}

// CheckSchema is read-only. Deployments run Migrate as an explicit one-off
// operation; ordinary managed startup and rollback must never execute DDL.
func CheckSchema(db *gorm.DB, backend string) (bool, error) {
	return checkSchema(db, backend, false)
}

// Additions include defaulted title-job metadata, optional ETF metadata and
// daily factor provenance. Existing rows keep empty, unverified provenance.
// Previous API images do not read these columns and can be rolled back.
// Keep the allowlist explicit; unrelated schema drift must still fail closed.
func CheckReleaseCompatibility(db *gorm.DB, backend string) (bool, error) {
	return checkSchema(db, backend, true)
}

func checkSchema(db *gorm.DB, backend string, allowAdditiveColumns bool) (bool, error) {
	sqlDB, err := db.DB()
	if err != nil {
		return false, err
	}
	if err := sqlDB.PingContext(db.Statement.Context); err != nil {
		return false, err
	}
	for _, model := range schemaModels(backend) {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(model); err != nil {
			return false, err
		}
		if !db.Migrator().HasTable(model) {
			return false, nil
		}
		columns, err := db.Migrator().ColumnTypes(model)
		if err != nil {
			return false, err
		}
		present := make(map[string]bool, len(columns))
		for _, column := range columns {
			present[column.Name()] = true
		}
		for _, field := range stmt.Schema.Fields {
			if field.DBName != "" && !field.IgnoreMigration && !present[field.DBName] {
				// Empty provenance preserves the meaning of legacy placeholder
				// factors. Older releases ignore this additive column.
				if allowAdditiveColumns && stmt.Table == "bar" && field.DBName == "adj_source" {
					continue
				}
				if allowAdditiveColumns && stmt.Table == "agent_session_binding" {
					switch field.DBName {
					case "title_generation", "title_settled_generation", "title_attempt", "title_lease_until_ms":
						continue
					}
				}
				if allowAdditiveColumns && stmt.Table == "instrument" {
					switch field.DBName {
					case "asset_type", "exchange", "full_name", "index_code", "index_name", "manager", "custodian", "etf_type", "management_fee", "setup_date":
						continue
					}
				}
				return false, nil
			}
		}
		for _, index := range stmt.Schema.ParseIndexes() {
			if !db.Migrator().HasIndex(model, index.Name) {
				return false, nil
			}
		}
	}
	return true, nil
}
