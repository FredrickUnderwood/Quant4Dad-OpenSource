package providers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository/datasource"
)

const (
	pageSize         = 1000
	maxPages         = 1000
	maxRows          = 200000
	maxResponseBytes = 8 << 20
	requestTimeout   = 30 * time.Second
)

var canonicalCode = regexp.MustCompile(`^(sh|sz|bj)\.[0-9]{6}$`)

// Every upstream request, including probe, catalog partitions, date windows and
// factor pages, passes through this gate. Redirects are never followed, even
// when a test supplies the HTTP transport.
type requester struct {
	client  *http.Client
	mu      sync.RWMutex
	limiter func(context.Context) error
}

func newRequester(client *http.Client) *requester {
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.ResponseHeaderTimeout = 15 * time.Second
		client = &http.Client{Transport: transport}
	}
	copy := *client
	copy.Timeout = requestTimeout
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &requester{client: &copy}
}

func (r *requester) SetRequestLimiter(limiter func(context.Context) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.limiter = limiter
}

func providerFailure(op string, status int, err error) error {
	return datasource.NewFetchError(op, status, nil, err)
}

func safeRequestError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return context.Canceled
	}
	var ne net.Error
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout() {
		return datasource.ErrProviderTimeout
	}
	return datasource.ErrProviderUpstream
}

func (r *requester) do(req *http.Request, out any) error {
	r.mu.RLock()
	wait := r.limiter
	r.mu.RUnlock()
	if err := req.Context().Err(); err != nil {
		return providerFailure("http", 0, safeRequestError(req.Context(), err))
	}
	if wait != nil {
		if err := wait(req.Context()); err != nil {
			return providerFailure("http", 0, safeRequestError(req.Context(), err))
		}
	}
	res, err := r.client.Do(req)
	if err != nil {
		return providerFailure("http", 0, safeRequestError(req.Context(), err))
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		classification := datasource.ErrProviderUpstream
		switch res.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			classification = datasource.ErrProviderAccess
		case http.StatusTooManyRequests:
			classification = datasource.ErrProviderRateLimit
		}
		return providerFailure("http", res.StatusCode, classification)
	}
	if res.ContentLength > maxResponseBytes {
		return providerFailure("read", res.StatusCode, datasource.ErrProviderResponse)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil {
		return providerFailure("read", res.StatusCode, safeRequestError(req.Context(), err))
	}
	if len(body) > maxResponseBytes || !utf8.Valid(body) {
		return providerFailure("read", res.StatusCode, datasource.ErrProviderResponse)
	}
	decoder := sonic.ConfigStd.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return providerFailure("parse", res.StatusCode, datasource.ErrProviderResponse)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return providerFailure("parse", res.StatusCode, datasource.ErrProviderResponse)
	}
	return nil
}

func validToken(token string) bool {
	return len(token) <= 4096 && strings.TrimSpace(token) == token && validText(token, 4096)
}

func validText(value string, max int) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > max {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func parseDate(value, layout string) (time.Time, error) {
	date, err := time.Parse(layout, value)
	if err != nil || date.Format(layout) != value || date.Year() < 1900 || date.Year() > 2200 {
		return time.Time{}, datasource.ErrProviderResponse
	}
	return date, nil
}

func dateRange(code string, period domain.BarPeriod, start, end time.Time) (time.Time, time.Time, error) {
	if !canonicalCode.MatchString(code) || period != domain.Bar1d && period != domain.Bar1w && period != domain.Bar1mo || start.IsZero() || end.IsZero() {
		return time.Time{}, time.Time{}, datasource.ErrProviderInput
	}
	from, e1 := parseDate(start.Format(time.DateOnly), time.DateOnly)
	to, e2 := parseDate(end.Format(time.DateOnly), time.DateOnly)
	if e1 != nil || e2 != nil || from.After(to) || to.Year()-from.Year() > 150 {
		return time.Time{}, time.Time{}, datasource.ErrProviderInput
	}
	return from, to, nil
}

func validateInstrument(it *domain.Instrument) error {
	if !canonicalCode.MatchString(it.Code) || strings.TrimSpace(it.Name) == "" || !validText(it.Name, 64) || it.AssetType != domain.AssetStock && it.AssetType != domain.AssetETF {
		return datasource.ErrProviderResponse
	}
	if it.Exchange == "" {
		it.Exchange = strings.ToUpper(it.Code[:2])
	}
	if it.Exchange != strings.ToUpper(it.Code[:2]) || it.AssetType == domain.AssetETF && it.Exchange == "BJ" {
		return datasource.ErrProviderResponse
	}
	if it.Status == "" {
		it.Status = "active"
	}
	if it.Status != "active" && it.Status != "delisted" && it.Status != "pending" {
		return datasource.ErrProviderResponse
	}
	for _, field := range []struct {
		value string
		bound int
	}{{it.Industry, 64}, {it.FullName, 255}, {it.IndexCode, 32}, {it.IndexName, 255}, {it.Manager, 128}, {it.Custodian, 128}, {it.ETFType, 32}} {
		if !validText(field.value, field.bound) {
			return datasource.ErrProviderResponse
		}
	}
	if it.ManagementFee != nil && (!finite(*it.ManagementFee) || *it.ManagementFee < 0) {
		return datasource.ErrProviderResponse
	}
	return nil
}

func validateBar(bar *domain.Bar, code string, period domain.BarPeriod, start, end time.Time) error {
	if bar.Code != code || bar.Period != period || bar.Date.Before(start) || bar.Date.After(end) {
		return datasource.ErrProviderResponse
	}
	for _, value := range []float64{bar.Open, bar.High, bar.Low, bar.Close, bar.AdjFactor} {
		if !finite(value) || value <= 0 {
			return datasource.ErrProviderResponse
		}
	}
	if bar.High < bar.Low || bar.Open > bar.High || bar.Open < bar.Low || bar.Close > bar.High || bar.Close < bar.Low || !finite(bar.Volume) || bar.Volume < 0 || !finite(bar.Amount) || bar.Amount < 0 {
		return datasource.ErrProviderResponse
	}
	return nil
}
