package datasource

import "errors"

// Provider errors are deliberately fixed and contain no URL, credential,
// request body, upstream response, or transport error text. Use errors.Is.
var (
	ErrProviderConfig      = errors.New("invalid datasource configuration")
	ErrProviderAccess      = errors.New("datasource access denied")
	ErrProviderRateLimit   = errors.New("datasource rate limited")
	ErrProviderTimeout     = errors.New("datasource request timed out")
	ErrProviderResponse    = errors.New("invalid datasource response")
	ErrProviderPagination  = errors.New("datasource pagination or truncation error")
	ErrProviderUpstream    = errors.New("datasource upstream request failed")
	ErrProviderInput       = errors.New("invalid datasource request")
	ErrProviderUnsupported = errors.New("datasource operation unsupported")
)
