package coldstore

import (
	"context"
	"io"
	"os"
	"path/filepath"
)

// LocalStore writes objects into a local directory, using the key as a relative path. It is
// for tests and local verification without OSS, not for production cold storage.
type LocalStore struct {
	baseDir string
}

func NewLocalStore(baseDir string) *LocalStore {
	return &LocalStore{baseDir: baseDir}
}

func (s *LocalStore) path(key string) string {
	return filepath.Join(s.baseDir, filepath.FromSlash(key))
}

func (s *LocalStore) Put(ctx context.Context, key string, r io.Reader) error {
	p := s.path(key)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return err
	}
	return f.Sync()
}

func (s *LocalStore) Exists(ctx context.Context, key string) (bool, error) {
	if _, err := os.Stat(s.path(key)); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
