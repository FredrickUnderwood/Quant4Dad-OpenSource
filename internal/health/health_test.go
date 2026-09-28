package health

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

func TestReadinessChecksDependencyWithDeadline(t *testing.T) {
	for _, healthy := range []bool{true, false} {
		w := httptest.NewRecorder()
		Ready(func(ctx context.Context) error {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("dependency check has no deadline")
			}
			if !healthy {
				return errors.New("secret DSN must not be returned")
			}
			return nil
		}).ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
		if healthy && w.Code != 200 || !healthy && (w.Code != 503 || w.Body.String() != "dependencies unavailable\n") {
			t.Fatal("incorrect readiness response")
		}
	}
}
