package providers

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository/datasource"
)

type httpProvider struct {
	*requester
	base       *url.URL
	token      string
	includeETF bool
}

func newHTTP(cfg config.DatasourceConfig) (datasource.Client, error) {
	return newHTTPWithClient(cfg, nil)
}

func newHTTPWithClient(cfg config.DatasourceConfig, client *http.Client) (*httpProvider, error) {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base == nil || base.Host == "" || base.Hostname() == "" || base.Scheme != "http" && base.Scheme != "https" || base.User != nil || base.RawQuery != "" || base.ForceQuery || strings.Contains(cfg.BaseURL, "#") || base.Opaque != "" || !validToken(cfg.Token) {
		return nil, datasource.ErrProviderConfig
	}
	base.Path = strings.TrimRight(base.Path, "/")
	base.RawPath = ""
	return &httpProvider{requester: newRequester(client), base: base, token: cfg.Token, includeETF: cfg.IncludeETF}, nil
}

func (*httpProvider) Name() string { return "http" }

type instrumentRecord struct {
	Code          string           `json:"code"`
	Name          string           `json:"name"`
	AssetType     domain.AssetType `json:"asset_type"`
	Exchange      string           `json:"exchange,omitempty"`
	Industry      string           `json:"industry,omitempty"`
	Status        string           `json:"status,omitempty"`
	ListedDate    string           `json:"listed_date,omitempty"`
	FullName      string           `json:"full_name,omitempty"`
	IndexCode     string           `json:"index_code,omitempty"`
	IndexName     string           `json:"index_name,omitempty"`
	Manager       string           `json:"manager,omitempty"`
	Custodian     string           `json:"custodian,omitempty"`
	ETFType       string           `json:"etf_type,omitempty"`
	ManagementFee *float64         `json:"management_fee,omitempty"`
	SetupDate     string           `json:"setup_date,omitempty"`
}

type barRecord struct {
	Code      string           `json:"code"`
	Period    domain.BarPeriod `json:"period"`
	Date      string           `json:"date"`
	Open      *float64         `json:"open"`
	High      *float64         `json:"high"`
	Low       *float64         `json:"low"`
	Close     *float64         `json:"close"`
	Volume    *float64         `json:"volume"`
	Amount    *float64         `json:"amount"`
	AdjFactor *float64         `json:"adj_factor,omitempty"`
}

type httpPage[T any] struct {
	Items      *[]T    `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func (r instrumentRecord) instrument() (*domain.Instrument, error) {
	it := &domain.Instrument{Code: r.Code, Name: r.Name, AssetType: r.AssetType, Exchange: r.Exchange, Industry: r.Industry, Status: r.Status, FullName: r.FullName, IndexCode: r.IndexCode, IndexName: r.IndexName, Manager: r.Manager, Custodian: r.Custodian, ETFType: r.ETFType, ManagementFee: r.ManagementFee}
	for _, field := range []struct {
		value  string
		target **time.Time
	}{{r.ListedDate, &it.ListedDate}, {r.SetupDate, &it.SetupDate}} {
		if field.value == "" {
			continue
		}
		date, err := parseDate(field.value, time.DateOnly)
		if err != nil {
			return nil, err
		}
		*field.target = &date
	}
	return it, validateInstrument(it)
}

func (r barRecord) bar(code string, period domain.BarPeriod, start, end time.Time) (*domain.Bar, error) {
	if r.Open == nil || r.High == nil || r.Low == nil || r.Close == nil || r.Volume == nil || r.Amount == nil {
		return nil, datasource.ErrProviderResponse
	}
	date, err := parseDate(r.Date, time.DateOnly)
	if err != nil {
		return nil, err
	}
	factor := 1.0
	if r.AdjFactor != nil {
		factor = *r.AdjFactor
	}
	bar := &domain.Bar{Code: r.Code, Period: r.Period, Date: date, Open: *r.Open, High: *r.High, Low: *r.Low, Close: *r.Close, Volume: *r.Volume, Amount: *r.Amount, AdjFactor: factor, AdjSource: "http"}
	return bar, validateBar(bar, code, period, start, end)
}

func (p *httpProvider) get(ctx context.Context, endpoint string, query url.Values, out any) error {
	endpointURL := *p.base
	endpointURL.Path += endpoint
	endpointURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpointURL.String(), nil)
	if err != nil {
		return datasource.ErrProviderInput
	}
	req.Header.Set("Accept", "application/json")
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	return p.do(req, out)
}

func getHTTPPage[T any](ctx context.Context, p *httpProvider, path string, query url.Values, cursor string) ([]T, string, error) {
	query.Set("cursor", cursor)
	query.Set("limit", strconv.Itoa(pageSize))
	var page httpPage[T]
	if err := p.get(ctx, path, query, &page); err != nil {
		return nil, "", err
	}
	if page.Items == nil || page.NextCursor == nil || !validText(*page.NextCursor, 2048) {
		return nil, "", datasource.ErrProviderResponse
	}
	if len(*page.Items) > pageSize {
		return nil, "", datasource.ErrProviderPagination
	}
	if len(*page.Items) == 0 && *page.NextCursor != "" {
		return nil, "", datasource.ErrProviderPagination
	}
	return *page.Items, *page.NextCursor, nil
}

func (p *httpProvider) Probe(ctx context.Context) error {
	items, _, err := getHTTPPage[instrumentRecord](ctx, p, "/instruments", url.Values{}, "")
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, record := range items {
		it, err := record.instrument()
		if err != nil {
			return err
		}
		if seen[it.Code] {
			return datasource.ErrProviderPagination
		}
		seen[it.Code] = true
	}
	// Probe validates one page and its continuation shape, without harvesting it.
	return nil
}

func (p *httpProvider) ListInstruments(ctx context.Context) ([]*domain.Instrument, error) {
	result := make([]*domain.Instrument, 0)
	seen := make(map[string]bool)
	err := walkPages(func(cursor string) (string, int, error) {
		items, next, err := getHTTPPage[instrumentRecord](ctx, p, "/instruments", url.Values{}, cursor)
		if err != nil {
			return "", 0, err
		}
		for _, record := range items {
			it, err := record.instrument()
			if err != nil {
				return "", 0, err
			}
			if seen[it.Code] {
				return "", 0, datasource.ErrProviderPagination
			}
			seen[it.Code] = true
			if it.AssetType != domain.AssetETF || p.includeETF {
				result = append(result, it)
			}
		}
		return next, len(items), nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Code < result[j].Code })
	return result, nil
}

func (p *httpProvider) FetchBars(ctx context.Context, code string, period domain.BarPeriod, start, end time.Time) ([]*domain.Bar, error) {
	start, end, err := dateRange(code, period, start, end)
	if err != nil {
		return nil, err
	}
	query := url.Values{"code": {code}, "period": {string(period)}, "start": {start.Format(time.DateOnly)}, "end": {end.Format(time.DateOnly)}}
	result := make([]*domain.Bar, 0)
	seen := make(map[time.Time]bool)
	err = walkPages(func(cursor string) (string, int, error) {
		items, next, err := getHTTPPage[barRecord](ctx, p, "/bars", query, cursor)
		if err != nil {
			return "", 0, err
		}
		for _, record := range items {
			bar, err := record.bar(code, period, start, end)
			if err != nil {
				return "", 0, err
			}
			if seen[bar.Date] {
				return "", 0, datasource.ErrProviderPagination
			}
			seen[bar.Date] = true
			result = append(result, bar)
		}
		return next, len(items), nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Date.Before(result[j].Date) })
	return result, nil
}

func (p *httpProvider) FetchInstrumentBars(ctx context.Context, it *domain.Instrument, period domain.BarPeriod, start, end time.Time) ([]*domain.Bar, error) {
	if it == nil || it.AssetType != domain.AssetStock && it.AssetType != domain.AssetETF {
		return nil, datasource.ErrProviderInput
	}
	if it.AssetType == domain.AssetETF && (!p.includeETF || period != domain.Bar1d) {
		return nil, datasource.ErrProviderUnsupported
	}
	return p.FetchBars(ctx, it.Code, period, start, end)
}

// An explicit empty cursor terminates even a full page. No guess about the
// upstream's total size is made; non-empty/repeated cursors fail closed.
func walkPages(fetch func(string) (string, int, error)) error {
	cursor := ""
	seen := map[string]bool{"": true}
	total := 0
	for page := 0; page < maxPages; page++ {
		next, count, err := fetch(cursor)
		if err != nil {
			return err
		}
		total += count
		if total > maxRows {
			return datasource.ErrProviderPagination
		}
		if next == "" {
			return nil
		}
		if seen[next] {
			return datasource.ErrProviderPagination
		}
		seen[next] = true
		cursor = next
	}
	return datasource.ErrProviderPagination
}

var _ datasource.Client = (*httpProvider)(nil)
var _ datasource.Prober = (*httpProvider)(nil)
var _ datasource.RequestLimitedClient = (*httpProvider)(nil)
var _ datasource.InstrumentBarsClient = (*httpProvider)(nil)
