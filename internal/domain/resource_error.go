package domain

import "errors"

// ErrResourceConflict means the caller's resource version is no longer current.
var ErrResourceConflict = errors.New("resource_version_conflict")
