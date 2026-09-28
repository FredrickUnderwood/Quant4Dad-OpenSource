package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
)

func TestKlineFilesPagingOwnershipResumeAndExpiry(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	repo, err := repository.NewAgentToolArtifactRepository(directory)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewAgentKlineFileService(repo)
	ctx := WithAgentExecution(context.Background(), domain.AgentToolAudit{ID: "audit", ActorID: "owner", SessionID: "session", RunID: "run-1"}, domain.AgentApprovalReceipt{})
	data := QueryKlineResult{Code: "sh.600809", Period: "1d", Count: 493, Bars: []*domain.Bar{}}
	for i := 0; i < data.Count; i++ {
		data.Bars = append(data.Bars, &domain.Bar{Code: data.Code, Period: domain.Bar1d, Date: time.Date(2024, 1, 1+i, 0, 0, 0, 0, time.UTC), Close: float64(i + 1)})
	}
	descriptor, err := svc.Save(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := sonic.Marshal(descriptor)
	if len(encoded) > 1024 || descriptor.Count != 493 || descriptor.FirstDate != "2024-01-01" {
		t.Fatalf("unexpected descriptor: %s", encoded)
	}
	// Restart the service and continue in a new Run of the same Session.
	svc = NewAgentKlineFileService(repo)
	ctx = WithAgentExecution(ctx, domain.AgentToolAudit{ID: "audit-2", ActorID: "owner", SessionID: "session", RunID: "run-2"}, domain.AgentApprovalReceipt{})
	seen := 0
	for {
		page, err := svc.Read(ctx, descriptor.FileID, seen, 100)
		if err != nil {
			t.Fatal(err)
		}
		if page.Count > 100 || page.Total != 493 || page.Offset != seen || page.Columns[4] != "close" {
			t.Fatalf("bad page: %+v", page)
		}
		for i, row := range page.Rows {
			if row[4] != float64(seen+i+1) {
				t.Fatal("missing or duplicate bar")
			}
		}
		seen = page.NextOffset
		if !page.HasMore {
			break
		}
	}
	if seen != 493 {
		t.Fatal(seen)
	}
	for _, audit := range []domain.AgentToolAudit{{ID: "x", ActorID: "other", SessionID: "session"}, {ID: "x", ActorID: "owner", SessionID: "other"}} {
		if _, err := svc.Read(WithAgentExecution(ctx, audit, domain.AgentApprovalReceipt{}), descriptor.FileID, 0, 50); err == nil {
			t.Fatal("cross-owner/session read accepted")
		}
	}
	for _, id := range []string{"../../etc/passwd", descriptor.File, "/tmp/anything"} {
		if _, err := svc.Read(ctx, id, 0, 50); err == nil {
			t.Fatal("path accepted")
		}
	}
	for _, args := range [][2]int{{-1, 50}, {494, 50}, {0, 101}, {0, 0}} {
		if _, err := svc.Read(ctx, descriptor.FileID, args[0], args[1]); err == nil {
			t.Fatal("invalid page accepted")
		}
	}
	if _, err := svc.Read(context.Background(), descriptor.FileID, 0, 50); err == nil {
		t.Fatal("unauthenticated read accepted")
	}
	if _, err := svc.Save(context.Background(), data); err == nil {
		t.Fatal("unauthenticated save accepted")
	}
	expired, _ := sonic.Marshal(storedKlineFile{"owner", "session", time.Now().Add(-time.Hour), data})
	ref, err := repo.Put(expired)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Read(ctx, ref[:64], 0, 50); err == nil {
		t.Fatal("expired file accepted")
	}
	old := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(filepath.Join(directory, ref), old, old); err != nil {
		t.Fatal(err)
	}
	removed, err := svc.Maintain(ctx, time.Now())
	if err != nil || removed != 1 {
		t.Fatalf("sweep: %d %v", removed, err)
	}
	if _, err := svc.Read(ctx, descriptor.FileID, 0, 50); err != nil {
		t.Fatal("live file removed", err)
	}
}
