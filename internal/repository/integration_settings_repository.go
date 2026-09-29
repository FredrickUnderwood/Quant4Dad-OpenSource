package repository

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"gopkg.in/yaml.v3"
)

var ErrIntegrationStore = errors.New("private integration settings unavailable")

// IntegrationSettingsRepository serializes writers and publishes only after an
// owner-only temporary file has been flushed and atomically renamed. Deployment
// is single-owner: two API processes must not share this file.
type IntegrationSettingsRepository struct {
	mu      sync.Mutex
	path    string
	current config.IntegrationDocument
}

func NewIntegrationSettingsRepository(path string, initial config.IntegrationDocument) (*IntegrationSettingsRepository, error) {
	if path == "" {
		return nil, ErrIntegrationStore
	}
	r := &IntegrationSettingsRepository{path: path, current: initial}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil || !privateSettingsInfo(info, false) || info.Size() > 64<<10 {
		return nil, ErrIntegrationStore
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil || !privateSettingsInfo(parent, true) {
		return nil, ErrIntegrationStore
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, ErrIntegrationStore
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var doc config.IntegrationDocument
	var extra any
	if dec.Decode(&doc) != nil || dec.Decode(&extra) != io.EOF || doc.SchemaVersion != 1 {
		return nil, ErrIntegrationStore
	}
	r.current = doc
	return r, nil
}

func (r *IntegrationSettingsRepository) Snapshot() config.IntegrationDocument {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current
}

func (r *IntegrationSettingsRepository) Replace(ctx context.Context, expected uint64, next config.IntegrationDocument) (config.IntegrationDocument, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return r.current, err
	}
	if expected != r.current.Revision {
		return r.current, domain.ErrResourceConflict
	}
	if expected == ^uint64(0) {
		return r.current, ErrIntegrationStore
	}
	next.SchemaVersion, next.Revision = 1, expected+1
	data, err := yaml.Marshal(next)
	if err != nil || len(data) > 64<<10 {
		return r.current, ErrIntegrationStore
	}
	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return r.current, ErrIntegrationStore
	}
	info, err := os.Lstat(dir)
	if err != nil || !privateSettingsInfo(info, true) {
		return r.current, ErrIntegrationStore
	}
	if info, err := os.Lstat(r.path); err == nil && !privateSettingsInfo(info, false) {
		return r.current, ErrIntegrationStore
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return r.current, ErrIntegrationStore
	}
	tmp, err := os.CreateTemp(dir, ".integrations-*")
	if err != nil {
		return r.current, ErrIntegrationStore
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil || ctx.Err() != nil {
		return r.current, ErrIntegrationStore
	}
	if err = os.Rename(name, r.path); err != nil {
		return r.current, ErrIntegrationStore
	}
	r.current = next
	// Directory sync is best effort on hosts that do not implement it. The file
	// is already durable and visible atomically; an API retry uses its revision.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return next, nil
}

func privateSettingsInfo(info os.FileInfo, directory bool) bool {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return false
	}
	if directory {
		if !info.IsDir() {
			return false
		}
	} else if !info.Mode().IsRegular() {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}
