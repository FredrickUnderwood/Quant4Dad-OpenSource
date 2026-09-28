package datasource

import "fmt"

// maxFetchErrBody bounds how much of a failed response we keep; enough to see
// an error JSON / rate-limit page without bloating the failures table.
const maxFetchErrBody = 2000

// FetchError wraps a provider failure together with the raw response context so
// data-sync can persist exactly what came back for later analysis.
// Client and NewsSource implementations should wrap upstream failures with
// NewFetchError so the sync record carries a slice of the raw response (a
// rate-limit page, an error JSON, …) and diagnosis doesn't require reproducing
// the request.
type FetchError struct {
	Op         string // "http" / "read" / "parse" / "api"
	StatusCode int    // HTTP status; 0 when the request never completed
	Body       string // raw response body (truncated); empty when unavailable
	Err        error  // underlying error
}

func (e *FetchError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Op, e.Err)
	}
	return e.Op
}

func (e *FetchError) Unwrap() error { return e.Err }

// NewFetchError builds a FetchError, truncating an over-long body. op is one of
// "http" / "read" / "parse" / "api"; status is the HTTP status code (pass 0 when
// the request never went out).
func NewFetchError(op string, status int, body []byte, err error) *FetchError {
	b := string(body)
	if len(b) > maxFetchErrBody {
		b = b[:maxFetchErrBody]
	}
	return &FetchError{Op: op, StatusCode: status, Body: b, Err: err}
}
