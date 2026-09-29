package repository

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
)

func integrationRepositoryFixture(t *testing.T, path string) *IntegrationSettingsRepository {
	t.Helper()
	cfg, err := config.Parse([]byte("server:\n  addr: ':8080'\n"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewIntegrationSettingsRepository(path, config.InitialIntegrations(cfg))
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestIntegrationSettingsAtomicPrivateRoundTripAndRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "integrations.yaml")
	r := integrationRepositoryFixture(t, path)
	c := r.Snapshot()
	c.Market.Tushare.Token = "synthetic-private-token"
	got, err := r.Replace(context.Background(), 0, c)
	if err != nil || got.Revision != 1 {
		t.Fatalf("save: %v", err)
	}
	for path, mode := range map[string]os.FileMode{path: 0600, filepath.Dir(path): 0700} {
		info, e := os.Stat(path)
		if e != nil || info.Mode().Perm() != mode {
			t.Fatal("unsafe private file permissions")
		}
	}
	reopened := integrationRepositoryFixture(t, path)
	if reopened.Snapshot() != got {
		t.Fatal("restart did not load saved snapshot")
	}
	c.Market.Tushare.Token = "replacement-must-not-commit"
	if _, err := r.Replace(context.Background(), 0, c); !errors.Is(err, domain.ErrResourceConflict) {
		t.Fatal("stale update accepted")
	}
	if integrationRepositoryFixture(t, path).Snapshot() != got {
		t.Fatal("stale update changed the file")
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	success, conflict := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := r.Replace(context.Background(), 1, got)
			mu.Lock()
			defer mu.Unlock()
			if e == nil {
				success++
			} else if errors.Is(e, domain.ErrResourceConflict) {
				conflict++
			} else {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if success != 1 || conflict != 7 || r.Snapshot().Revision != 2 {
		t.Fatal("concurrent update lost revision protection")
	}
}
func TestIntegrationSettingsFailedWriteAndUnsafeFiles(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "integrations.yaml")
	r := integrationRepositoryFixture(t, path)
	before := r.Snapshot()
	if _, err := r.Replace(context.Background(), 0, before); !errors.Is(err, ErrIntegrationStore) {
		t.Fatal("public parent was accepted")
	}
	info, _ := os.Stat(parent)
	if info.Mode().Perm() != 0755 {
		t.Fatal("repository changed an existing directory's permissions")
	}
	if r.Snapshot() != before {
		t.Fatal("failed write published in memory")
	}
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("schema_version: 1\nrevision: 3\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewIntegrationSettingsRepository(path, before); !errors.Is(err, ErrIntegrationStore) {
		t.Fatal("public settings accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("schema_version: 1\nunknown: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewIntegrationSettingsRepository(path, before); !errors.Is(err, ErrIntegrationStore) {
		t.Fatal("unknown fields accepted")
	}
	link := filepath.Join(parent, "link.yaml")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewIntegrationSettingsRepository(link, before); !errors.Is(err, ErrIntegrationStore) {
		t.Fatal("settings symlink accepted")
	}
}

func TestIntegrationSettingsLoadRejectsUnsafeExistingParent(t *testing.T) {
	for _, name := range []string{"public", "writable", "symlink"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			parent := filepath.Join(root, "private")
			path := filepath.Join(parent, "integrations.yaml")
			r := integrationRepositoryFixture(t, path)
			saved, err := r.Replace(context.Background(), 0, r.Snapshot())
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			loadPath := path
			switch name {
			case "public":
				err = os.Chmod(parent, 0755)
			case "writable":
				err = os.Chmod(parent, 0777)
			case "symlink":
				link := filepath.Join(root, "linked-private")
				err = os.Symlink(parent, link)
				loadPath = filepath.Join(link, "integrations.yaml")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NewIntegrationSettingsRepository(loadPath, saved); !errors.Is(err, ErrIntegrationStore) {
				t.Fatal("unsafe existing settings directory accepted", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("failed load changed durable settings", err)
			}
		})
	}
}
