// Package coldstore abstracts the cold store: where the archive job writes expired data
// once and almost never reads it back. It currently offers an Alibaba Cloud OSS
// implementation (oss.go) and a local file implementation (local.go, for tests and
// environments without OSS).
package coldstore

import (
	"context"
	"io"
)

// ColdStore is the cold store's minimal interface. The archive job needs only two
// capabilities, writing an object and confirming an object is there: Put uploads, Exists
// verifies the write succeeded, and only then may the corresponding data be deleted from
// MySQL.
type ColdStore interface {
	// Put writes all of r's content to key, overwriting anything already there.
	Put(ctx context.Context, key string, r io.Reader) error
	// Exists reports whether key is present, for the post-upload verification.
	Exists(ctx context.Context, key string) (bool, error)
}
