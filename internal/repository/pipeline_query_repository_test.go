package repository

import (
	"context"
	"os"
	"testing"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"gorm.io/gorm"
)

func TestPipelineGetAgentSQLite(t *testing.T) { assertPipelineGetAgent(t, modelRepositoryDB(t)) }

func TestPipelineGetAgentMySQL(t *testing.T) {
	dsn := os.Getenv("QUANT4DAD_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("QUANT4DAD_TEST_MYSQL_DSN is not set")
	}
	db, err := Open(config.StorageConfig{Backend: config.StorageBackendMySQL, MySQL: config.MySQLConfig{DSN: dsn}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	assertPipelineGetAgent(t, db)
}

func assertPipelineGetAgent(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&domain.Pipeline{}, &domain.PipelineNode{}, &domain.PipelineEdge{}, &domain.NewsSubscription{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	repo := NewPipelineRepository(tx)
	pipeline := domain.Pipeline{Name: "bounded-agent-pipeline", Status: domain.PipelineStatusDraft, Version: 1,
		Nodes: []domain.PipelineNode{{NodeKey: "first", Type: "keyword_filter", Config: []byte(`{"keywords":["测试"]}`)},
			{NodeKey: "second", Type: "keyword_filter", Config: []byte(`{"keywords":["fixture"]}`)}},
		Sources: []string{"cls"}}
	// Even the empty-edge query must parse: MySQL rejects an unquoted CONDITION
	// before it discovers that this pipeline has no edges.
	if err := repo.Create(context.Background(), &pipeline); err != nil {
		t.Fatal(err)
	}
	for _, withEdge := range []bool{false, true} {
		condition := `{"field":"text","op":"contains","value":"测试"}`
		if withEdge {
			edge := domain.PipelineEdge{PipelineID: pipeline.ID, FromNodeKey: "first", ToNodeKey: "second", Condition: []byte(condition)}
			if err := tx.Create(&edge).Error; err != nil {
				t.Fatal(err)
			}
		}
		got, err := repo.GetAgent(context.Background(), pipeline.ID)
		if err != nil {
			t.Fatalf("withEdge=%v: %v", withEdge, err)
		}
		if got.ID != pipeline.ID || got.Version != 1 || len(got.Nodes) != 2 || len(got.Sources) != 1 || got.Sources[0] != "cls" {
			t.Fatalf("incomplete pipeline snapshot: %+v", got)
		}
		if string(got.Nodes[0].Config) != string(pipeline.Nodes[0].Config) {
			t.Fatal("node configuration changed")
		}
		if withEdge {
			if len(got.Edges) != 1 || string(got.Edges[0].Condition) != condition {
				t.Fatalf("edge condition changed: %+v", got.Edges)
			}
		} else if len(got.Edges) != 0 {
			t.Fatal("unexpected edge")
		}
	}
}
