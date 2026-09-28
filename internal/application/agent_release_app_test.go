package application

import (
	"context"
	"errors"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/service"
	"testing"
)

type changingRelease struct {
	manifest config.AgentRunManifest
	err      error
}

func (r *changingRelease) Current(context.Context) (config.AgentRunManifest, error) {
	return r.manifest, r.err
}
func TestAgentReleaseChangeInvalidatesOldAuthorityWithoutAPIRecreation(t *testing.T) {
	app, _, _, runtime, _, session := runAppFixture(t)
	r := &changingRelease{manifest: app.manifest}
	app.SetReleaseSource(service.NewAgentReleaseService(r))
	ctx := context.Background()
	input := runTestMessage()
	first, err := app.Send(ctx, "local-user", session, input)
	if err != nil {
		t.Fatal(err)
	}
	r.manifest.AgentRuntimeVersion = "new-runtime"
	if _, err = app.Authorization(ctx, first.RunID); !errors.Is(err, service.ErrAgentRunRejected) {
		t.Fatal("old authority survived release switch", err)
	}
	input.ClientRequestID = "next-release"
	if _, err = app.Send(ctx, "local-user", session, input); err != nil {
		t.Fatal("new release not admitted", err)
	}
	if len(runtime.tokens) != 2 {
		t.Fatal("unexpected dispatch count")
	}
	r.err = errors.New("invalid signature")
	input.ClientRequestID = "invalid-release"
	if _, err = app.Send(ctx, "local-user", session, input); !errors.Is(err, service.ErrAgentRunUnavailable) {
		t.Fatal("invalid release did not fail closed", err)
	}
	if len(runtime.tokens) != 2 {
		t.Fatal("invalid release dispatched")
	}
}
