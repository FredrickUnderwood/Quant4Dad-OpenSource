package coldstore

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// LocalStore implements the same immutable publication contract for tests.
type LocalStore struct{ baseDir string }

func NewLocalStore(baseDir string) *LocalStore { return &LocalStore{baseDir: baseDir} }
func (s *LocalStore) path(key string) string {
	return filepath.Join(s.baseDir, filepath.FromSlash(key))
}
func (s *LocalStore) Verify(ctx context.Context, key string, digest Digest) error {
	if !validObject(key, digest) {
		return ErrInvalidObject
	}
	p := s.path(key)
	info, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return ErrObjectNotFound
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrInvalidObject
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return verifyReader(ctx, f, digest)
}
func (s *LocalStore) PutImmutable(ctx context.Context, key string, r io.ReadSeeker, digest Digest) error {
	if err := validateSource(ctx, key, r, digest); err != nil {
		return err
	}
	if err := s.Verify(ctx, key, digest); err == nil {
		return nil
	} else if !errors.Is(err, ErrObjectNotFound) {
		return err
	}
	p := s.path(key)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".archive-*")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	defer f.Close()
	if _, err = io.Copy(f, contextReader{ctx, r}); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// Link publishes only a fully synced file and never truncates an existing key.
	if err = os.Link(temporary, p); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	dir, err := os.Open(filepath.Dir(p))
	if err != nil {
		return err
	}
	err = dir.Sync()
	_ = dir.Close()
	if err != nil {
		return err
	}
	return s.Verify(ctx, key, digest)
}
