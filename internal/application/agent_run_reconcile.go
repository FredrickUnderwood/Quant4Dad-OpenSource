package application

import (
	"context"
	"github.com/quant4dad/internal/agentbridge"
	"time"
)

// Reconcile only queries/settles existing identities. It cannot reconstruct a
// prompt, issue a fresh grant, extend a deadline or dispatch a business Tool.
func (a *AgentRunApplication) Reconcile(ctx context.Context, actor, id string) (agentbridge.Run, error) {
	row, err := a.runs.Get(ctx, actor, id)
	if err != nil {
		return agentbridge.Run{}, err
	}
	v, err := a.runtime.Get(ctx, row.SessionID, id)
	if err != nil {
		return agentbridge.Run{}, err
	}
	if err = a.confirmProjection(ctx, row, v); err != nil {
		return agentbridge.Run{}, err
	}
	if v.State == "recovering" {
		v, err = a.runtime.Reconcile(ctx, row.SessionID, id)
		if err != nil {
			return agentbridge.Run{}, err
		}
		err = a.confirmProjection(ctx, row, v)
	}
	return v, err
}

func (a *AgentRunApplication) ReconcileBatch(ctx context.Context, after string) (string, error) {
	rows, err := a.runs.Scan(ctx, after)
	if err != nil {
		return after, err
	}
	var first error
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return after, err
		}
		check, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := a.Reconcile(check, row.ActorID, row.ID)
		cancel()
		if first == nil {
			first = err
		}
		after = row.ID
	}
	if len(rows) < 32 {
		after = ""
	}
	return after, first
}

func (a *AgentRunApplication) StartReconciler() func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		after := ""
		for {
			after, _ = a.ReconcileBatch(ctx, after)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}
