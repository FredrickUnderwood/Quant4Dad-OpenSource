package application

import (
	"context"
	"errors"
	"testing"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository/datasource"
)

func TestDataSyncUnavailableFailsBeforeTaskCreation(t *testing.T) {
	for _, client := range []datasource.Client{nil, datasource.Unavailable()} {
		app, tasks := etfSyncApp(t, &etfSyncFixture{})
		app.client = client
		task := &domain.DataSyncTask{}
		out, err := app.Enqueue(context.Background(), task)
		if !errors.Is(err, datasource.ErrNoProvider) || out != nil {
			t.Fatalf("unexpected enqueue result: %+v %v", out, err)
		}
		if task.ID != 0 || task.Status != "" || !task.StartDate.IsZero() {
			t.Fatalf("unavailable sync mutated task: %+v", task)
		}
		rows, err := tasks.List(context.Background(), 10)
		if err != nil || len(rows) != 0 {
			t.Fatalf("unavailable sync persisted a task: %+v %v", rows, err)
		}
	}
}
