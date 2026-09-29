// Package coldstore publishes and verifies immutable, content-addressed archives.
package coldstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path"
	"strings"
)

var (
	ErrInvalidObject    = errors.New("archive_object_invalid")
	ErrObjectNotFound   = errors.New("archive_object_not_found")
	ErrObjectConflict   = errors.New("archive_object_content_mismatch")
	ErrStoreUnavailable = errors.New("archive_store_unavailable")
	ErrProbeRejected    = errors.New("archive_probe_permission_denied")
)

// Digest identifies the bytes actually stored, including gzip framing.
type Digest struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// ColdStore never replaces an object with different bytes. Verify checks actual
// content, not merely object existence; deletion is forbidden until it succeeds.
type ColdStore interface {
	PutImmutable(context.Context, string, io.ReadSeeker, Digest) error
	Verify(context.Context, string, Digest) error
}

func validObject(key string, digest Digest) bool {
	hash, err := hex.DecodeString(digest.SHA256)
	if err != nil || len(hash) != sha256.Size || hex.EncodeToString(hash) != digest.SHA256 || digest.Size < 0 || digest.Size > 1<<30 {
		return false
	}
	if key == "" || strings.HasPrefix(key, "/") || path.Clean(key) != key || strings.ContainsAny(key, "\\\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	// The leaf itself carries its content hash. Bucket versioning may ignore an
	// OSS forbid-overwrite header; all writers of this key must still use one body.
	return strings.Contains(path.Base(key), digest.SHA256)
}

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func verifyReader(ctx context.Context, reader io.Reader, expected Digest) error {
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(contextReader{ctx, reader}, expected.Size+1))
	if err != nil {
		return err
	}
	if n != expected.Size || hex.EncodeToString(h.Sum(nil)) != expected.SHA256 {
		return ErrObjectConflict
	}
	return nil
}
func validateSource(ctx context.Context, key string, reader io.ReadSeeker, digest Digest) error {
	if !validObject(key, digest) || reader == nil {
		return ErrInvalidObject
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return ErrInvalidObject
	}
	if err := verifyReader(ctx, reader, digest); err != nil {
		return err
	}
	_, err := reader.Seek(0, io.SeekStart)
	return err
}
