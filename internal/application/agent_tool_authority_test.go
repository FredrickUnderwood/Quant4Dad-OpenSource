package application

import (
	"context"
	"testing"

	"github.com/quant4dad/internal/domain"
)

func TestAgentToolAuthorityRequiresCurrentPolicyAndLiveBinding(t *testing.T) {
	app, _, _, runtime, db, session := runAppFixture(t)
	ctx := context.Background()
	submission, err := app.Send(ctx, "local-user", session, runTestMessage())
	if err != nil {
		t.Fatal(err)
	}
	claims, err := app.Authorization(ctx, submission.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if allowed, _ := app.ToolAuthority(ctx, claims); allowed {
		t.Fatal("completed Run retained execution authority")
	}
	runtime.mu.Lock()
	run := runtime.runs[submission.RunID]
	run.State = "running"
	run.Terminal = false
	runtime.runs[submission.RunID] = run
	runtime.mu.Unlock()
	if allowed, err := app.ToolAuthority(ctx, claims); !allowed || err != nil {
		t.Fatal("live exact binding rejected", err)
	}
	tampered := claims
	tampered.JTI = "00000000000000000000000000000000"
	if allowed, _ := app.ToolAuthority(ctx, tampered); allowed {
		t.Fatal("different original JTI accepted")
	}
	runtime.mu.Lock()
	run.ExecutionEnvelopeDigest = "sha256:bad"
	runtime.runs[submission.RunID] = run
	runtime.mu.Unlock()
	if allowed, _ := app.ToolAuthority(ctx, claims); allowed {
		t.Fatal("Runtime binding mismatch accepted")
	}
	runtime.mu.Lock()
	run.ExecutionEnvelopeDigest = claims.EnvelopeDigest
	runtime.runs[submission.RunID] = run
	runtime.mu.Unlock()
	if err = db.Model(&domain.AgentRequestBinding{}).Where("id = ?", submission.RunID).Update("revoked", true).Error; err != nil {
		t.Fatal(err)
	}
	if allowed, _ := app.ToolAuthority(ctx, claims); allowed {
		t.Fatal("revoked grant accepted")
	}
}
