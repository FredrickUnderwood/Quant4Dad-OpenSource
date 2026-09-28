package repository

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/quant4dad/internal/domain"
)

func TestAgentBacktestClaimLeaseAndAtomicOutcome(t *testing.T) {
	previousZone := time.Local
	time.Local = time.FixedZone("fixture+08", 8*60*60)
	t.Cleanup(func() { time.Local = previousZone })
	db := modelRepositoryDB(t)
	if err := db.AutoMigrate(&domain.BacktestJob{}, &domain.BacktestResult{}, &domain.Trade{}, &domain.EquityPoint{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	ctx := context.Background()
	repo := NewBacktestRepository(db)
	legacy := &domain.BacktestJob{Status: domain.BacktestStatusPending}
	job := &domain.BacktestJob{Status: domain.BacktestStatusPending, AgentRun: true, AgentSnapshot: `{"fixed":true}`}
	if err := repo.CreateJob(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	claimed := make(chan *domain.BacktestJob, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			row, err := repo.ClaimAgent(ctx, "worker-nonce")
			if err != nil {
				t.Error(err)
			}
			if row != nil {
				claimed <- row
			}
		}()
	}
	wg.Wait()
	close(claimed)
	if len(claimed) != 1 {
		t.Fatal("job claimed more than once", len(claimed))
	}
	owned := <-claimed
	if owned.ID != job.ID || owned.AgentSnapshot != job.AgentSnapshot {
		t.Fatal("claim lost snapshot")
	}
	wrong := *owned
	wrong.AgentWorkerNonce = "other-worker"
	if err := repo.CompleteAgent(ctx, &wrong, &domain.BacktestResult{}, nil, nil); !errors.Is(err, ErrAgentBacktestLease) {
		t.Fatal("foreign worker committed", err)
	}
	// A result collision after status/trade writes must roll all of them back.
	if err := db.Create(&domain.BacktestResult{JobID: job.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteAgent(ctx, owned, &domain.BacktestResult{}, []*domain.Trade{{Code: "sh.600519"}}, nil); err == nil {
		t.Fatal("expected injected result collision")
	}
	var current domain.BacktestJob
	db.First(&current, job.ID)
	var trades int64
	db.Model(&domain.Trade{}).Where("job_id = ?", job.ID).Count(&trades)
	if current.Status != domain.BacktestStatusRunning || trades != 0 {
		t.Fatal("partial outcome escaped rollback")
	}
	if err := db.Delete(&domain.BacktestResult{}, "job_id = ?", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteAgent(ctx, owned, &domain.BacktestResult{}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteAgent(ctx, owned, &domain.BacktestResult{}, nil, nil); !errors.Is(err, ErrAgentBacktestLease) {
		t.Fatal("outcome committed twice")
	}
	if err := repo.FailAgent(ctx, owned, "late_failure"); !errors.Is(err, ErrAgentBacktestLease) {
		t.Fatal("late failure falsely reported persistence", err)
	}
	db.First(&current, job.ID)
	if current.Status != domain.BacktestStatusSucceed {
		t.Fatal("late failure overwrote success")
	}
	expired := &domain.BacktestJob{Status: domain.BacktestStatusPending, AgentRun: true}
	if err := repo.CreateJob(ctx, expired); err != nil {
		t.Fatal(err)
	}
	stale, err := repo.ClaimAgent(ctx, "expired-worker")
	if err != nil || stale == nil {
		t.Fatal(err)
	}
	if err := db.Model(&domain.BacktestJob{}).Where("id = ?", stale.ID).Update("agent_lease_until", time.Now().UTC().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteAgent(ctx, stale, &domain.BacktestResult{}, nil, nil); !errors.Is(err, ErrAgentBacktestLease) {
		t.Fatal("expired lease committed")
	}
	if err := repo.FailExpiredAgent(ctx); err != nil {
		t.Fatal(err)
	}
	if row, err := repo.ClaimAgent(ctx, "replacement"); err != nil || row != nil {
		t.Fatal("interrupted compute automatically repeated", err)
	}
}
