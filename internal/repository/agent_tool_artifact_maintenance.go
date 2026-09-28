package repository

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// All readers/writers cooperate through flock, including independent Go
// instances sharing the volume. Never remove or replace the lock file.
func (r *AgentToolArtifactRepository) lock(ctx context.Context, exclusive bool) (func(), error) {
	fd, err := unix.Open(filepath.Join(r.directory, ".artifact-lock"), unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, ErrToolArtifact
	}
	f := os.NewFile(uintptr(fd), "artifact-lock")
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0777 != 0600 || st.Nlink != 1 || int(st.Uid) != os.Getuid() {
		f.Close()
		return nil, ErrToolArtifact
	}
	kind := unix.LOCK_SH
	if exclusive {
		kind = unix.LOCK_EX
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for {
		err = unix.Flock(fd, kind|unix.LOCK_NB)
		if err == nil {
			return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = f.Close() }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			f.Close()
			return nil, ErrToolArtifact
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			f.Close()
			return nil, ErrToolArtifact
		case <-timer.C:
		}
	}
}

// Sweep only removes unreferenced regular artifacts/abandoned temp files older
// than a day. It skips foreign entries and symlinks and fsyncs deletions.
func (r *AgentToolArtifactRepository) Sweep(ctx context.Context, now time.Time, keep func(context.Context, string) (bool, error)) (int, error) {
	if keep == nil {
		return 0, ErrToolArtifact
	}
	unlock, err := r.lock(ctx, true)
	if err != nil {
		return 0, err
	}
	defer unlock()
	dir, err := os.Open(r.directory)
	if err != nil {
		return 0, ErrToolArtifact
	}
	defer dir.Close()
	removed := 0
	defer func() {
		if removed > 0 {
			_ = dir.Sync()
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		entries, err := dir.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return removed, ErrToolArtifact
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return removed, err
			}
			name := entry.Name()
			if !toolArtifactName.MatchString(name) && !strings.HasPrefix(name, ".result-") {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return removed, ErrToolArtifact
			}
			if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !info.ModTime().Before(now.Add(-24*time.Hour)) {
				continue
			}
			referenced, err := keep(ctx, name)
			if err != nil {
				return removed, err
			}
			if referenced {
				continue
			}
			if err := os.Remove(filepath.Join(r.directory, name)); err != nil {
				return removed, ErrToolArtifact
			}
			removed++
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	if removed > 0 {
		if err := dir.Sync(); err != nil {
			return removed, ErrToolArtifact
		}
	}
	return removed, nil
}
