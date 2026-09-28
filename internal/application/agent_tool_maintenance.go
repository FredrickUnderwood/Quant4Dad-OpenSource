package application

import (
	"context"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/service"
	"go.uber.org/zap"
	"time"
)

type temporaryArtifactStore interface {
	Maintain(context.Context, time.Time) (int, error)
}

func StartAgentToolMaintenance(svc *service.AgentToolAuditService, files ...temporaryArtifactStore) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			batch, stop := context.WithTimeout(ctx, 5*time.Second)
			changed, removed, err := svc.Maintain(batch, time.Now().UTC())
			for _, file := range files {
				if file != nil {
					n, fileErr := file.Maintain(batch, time.Now().UTC())
					removed += n
					if err == nil {
						err = fileErr
					}
				}
			}
			stop()
			if err == nil && (changed > 0 || removed > 0) {
				logger.L().Info("agent tool maintenance completed", zap.Int64("settled", changed), zap.Int("artifacts_removed", removed))
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}
