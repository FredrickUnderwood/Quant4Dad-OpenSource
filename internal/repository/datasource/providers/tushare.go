package providers

import (
	"bytes"
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository/datasource"
)

const (
	tushareEndpoint = "https://api.tushare.pro"
	stockFields     = "ts_code,name,industry,exchange,list_status,list_date"
	etfFields       = "ts_code,csname,extname,cname,index_code,index_name,setup_date,list_date,list_status,exchange,mgr_name,custod_name,mgt_fee,etf_type"
	barFields       = "ts_code,trade_date,open,high,low,close,vol,amount"
	factorFields    = "ts_code,trade_date,adj_factor"
)

type tushareProvider struct {
	*requester
	endpoint     string
	token        string
	includeETF   bool
	etfMu        sync.RWMutex
	verifiedETFs map[string]bool
}

func newTushare(cfg config.DatasourceConfig) (datasource.Client, error) {
	// Never send an official token to a configurable host, even if it uses TLS.
	if cfg.BaseURL != "" && cfg.BaseURL != tushareEndpoint && cfg.BaseURL != tushareEndpoint+"/" {
		return nil, datasource.ErrProviderConfig
	}
	return newTushareWithEndpoint(cfg, tushareEndpoint, nil)
}

// Only same-package tests can substitute the endpoint/transport. Configuration
// never exposes this injection path.
func newTushareWithEndpoint(cfg config.DatasourceConfig, endpoint string, client *http.Client) (*tushareProvider, error) {
	if cfg.Token == "" || !validToken(cfg.Token) {
		return nil, datasource.ErrProviderConfig
	}
	return &tushareProvider{requester: newRequester(client), endpoint: endpoint, token: cfg.Token, includeETF: cfg.IncludeETF, verifiedETFs: make(map[string]bool)}, nil
}

func (*tushareProvider) Name() string { return "tushare" }

type tsRequest struct {
	APIName string         `json:"api_name"`
	Token   string         `json:"token"`
	Params  map[string]any `json:"params"`
	Fields  string         `json:"fields"`
}

type tsResponse struct {
	RequestID string `json:"request_id"`
	Code      *int   `json:"code"`
	Message   string `json:"msg"`
	Data      *struct {
		Fields *[]string `json:"fields"`
		Items  *[][]any  `json:"items"`
	} `json:"data"`
}

type tsRow map[string]any

func (p *tushareProvider) call(ctx context.Context, api, fields string, params map[string]any) ([]tsRow, error) {
	body, err := sonic.Marshal(tsRequest{APIName: api, Token: p.token, Params: params, Fields: fields})
	if err != nil {
		return nil, datasource.ErrProviderInput
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, datasource.ErrProviderInput
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	var response tsResponse
	if err := p.do(req, &response); err != nil {
		return nil, err
	}
	if response.Code == nil {
		return nil, datasource.ErrProviderResponse
	}
	if *response.Code != 0 {
		kind := datasource.ErrProviderUpstream
		if *response.Code == 2002 {
			kind = datasource.ErrProviderAccess
		}
		return nil, providerFailure("api", http.StatusOK, kind)
	}
	if response.Data == nil || response.Data.Fields == nil || response.Data.Items == nil || len(*response.Data.Fields) > 128 {
		return nil, datasource.ErrProviderResponse
	}
	seen := make(map[string]bool)
	for _, field := range *response.Data.Fields {
		if field == "" || seen[field] || !validText(field, 64) {
			return nil, datasource.ErrProviderResponse
		}
		seen[field] = true
	}
	for _, field := range strings.Split(fields, ",") {
		if !seen[field] {
			return nil, datasource.ErrProviderResponse
		}
	}
	rows := make([]tsRow, 0, len(*response.Data.Items))
	for _, values := range *response.Data.Items {
		if len(values) != len(*response.Data.Fields) {
			return nil, datasource.ErrProviderResponse
		}
		row := make(tsRow, len(values))
		for i, field := range *response.Data.Fields {
			row[field] = values[i]
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func stringValue(row tsRow, key string, optional bool) (string, error) {
	value, exists := row[key]
	if optional && value == nil && exists {
		return "", nil
	}
	text, ok := value.(string)
	if !ok || !optional && text == "" {
		return "", datasource.ErrProviderResponse
	}
	return text, nil
}

func numberValue(row tsRow, key string) (float64, error) {
	value, ok := row[key].(float64)
	if !ok || !finite(value) {
		return 0, datasource.ErrProviderResponse
	}
	return value, nil
}

func canonicalTSCode(value string) (string, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || parts[1] != "SH" && parts[1] != "SZ" && parts[1] != "BJ" {
		return "", datasource.ErrProviderResponse
	}
	code := strings.ToLower(parts[1]) + "." + parts[0]
	if !canonicalCode.MatchString(code) {
		return "", datasource.ErrProviderResponse
	}
	return code, nil
}

func tsCode(code string) string { return code[3:] + "." + strings.ToUpper(code[:2]) }

func stockInstrument(row tsRow) (*domain.Instrument, error) {
	it := &domain.Instrument{AssetType: domain.AssetStock}
	var err error
	for _, field := range []struct {
		key      string
		target   *string
		optional bool
	}{{"ts_code", &it.Code, false}, {"name", &it.Name, false}, {"industry", &it.Industry, true}, {"exchange", &it.Exchange, false}, {"list_status", &it.Status, false}} {
		*field.target, err = stringValue(row, field.key, field.optional)
		if err != nil {
			return nil, err
		}
	}
	it.Code, err = canonicalTSCode(it.Code)
	if err != nil {
		return nil, err
	}
	exchange := map[string]string{"SSE": "SH", "SZSE": "SZ", "BSE": "BJ"}[it.Exchange]
	if exchange == "" || it.Status != "L" {
		return nil, datasource.ErrProviderResponse
	}
	it.Exchange, it.Status = exchange, "active"
	dateText, err := stringValue(row, "list_date", false)
	if err != nil {
		return nil, err
	}
	date, err := parseDate(dateText, "20060102")
	if err != nil {
		return nil, err
	}
	it.ListedDate = &date
	return it, validateInstrument(it)
}

func etfInstrument(row tsRow) (*domain.Instrument, error) {
	it := &domain.Instrument{AssetType: domain.AssetETF}
	var err error
	for _, field := range []struct {
		key      string
		target   *string
		optional bool
	}{{"ts_code", &it.Code, false}, {"extname", &it.Name, true}, {"cname", &it.FullName, true}, {"exchange", &it.Exchange, false}, {"index_code", &it.IndexCode, true}, {"index_name", &it.IndexName, true}, {"mgr_name", &it.Manager, true}, {"custod_name", &it.Custodian, true}, {"etf_type", &it.ETFType, true}, {"list_status", &it.Status, false}} {
		*field.target, err = stringValue(row, field.key, field.optional)
		if err != nil {
			return nil, err
		}
	}
	if it.Name == "" {
		it.Name, err = stringValue(row, "csname", false)
		if err != nil {
			return nil, err
		}
	}
	it.Code, err = canonicalTSCode(it.Code)
	if err != nil {
		return nil, err
	}
	if it.Status != "L" || it.Exchange != "SH" && it.Exchange != "SZ" {
		return nil, datasource.ErrProviderResponse
	}
	it.Status = "active"
	for _, field := range []struct {
		key      string
		target   **time.Time
		optional bool
	}{{"list_date", &it.ListedDate, false}, {"setup_date", &it.SetupDate, true}} {
		value, err := stringValue(row, field.key, field.optional)
		if err != nil {
			return nil, err
		}
		if value == "" {
			continue
		}
		date, err := parseDate(value, "20060102")
		if err != nil {
			return nil, err
		}
		*field.target = &date
	}
	if row["mgt_fee"] != nil {
		fee, err := numberValue(row, "mgt_fee")
		if err != nil {
			return nil, err
		}
		it.ManagementFee = &fee
	}
	return it, validateInstrument(it)
}

func (p *tushareProvider) Probe(ctx context.Context) error {
	rows, err := p.call(ctx, "stock_basic", stockFields, map[string]any{"ts_code": "000001.SZ", "list_status": "L"})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return datasource.ErrProviderResponse
	}
	it, err := stockInstrument(rows[0])
	if err != nil {
		return err
	}
	if it.Code != "sz.000001" {
		return datasource.ErrProviderResponse
	}
	return nil
}

func (p *tushareProvider) ListInstruments(ctx context.Context) ([]*domain.Instrument, error) {
	result := make([]*domain.Instrument, 0)
	seen := make(map[string]bool)
	collect := func(api, fields, exchange string, cap int, parse func(tsRow) (*domain.Instrument, error)) error {
		rows, err := p.call(ctx, api, fields, map[string]any{"exchange": exchange, "list_status": "L"})
		if err != nil {
			return err
		}
		// These catalog APIs do not document offset pagination. Reaching the
		// official cap is ambiguous, even if the final row happens to fit.
		if len(rows) >= cap {
			return datasource.ErrProviderPagination
		}
		for _, row := range rows {
			it, err := parse(row)
			if err != nil {
				return err
			}
			expected := exchange
			if api == "stock_basic" {
				expected = map[string]string{"SSE": "SH", "SZSE": "SZ", "BSE": "BJ"}[exchange]
			}
			if it.Exchange != expected {
				return datasource.ErrProviderResponse
			}
			if seen[it.Code] {
				return datasource.ErrProviderPagination
			}
			seen[it.Code] = true
			result = append(result, it)
		}
		return nil
	}
	for _, exchange := range []string{"SSE", "SZSE", "BSE"} {
		if err := collect("stock_basic", stockFields, exchange, 6000, stockInstrument); err != nil {
			return nil, err
		}
	}
	if p.includeETF {
		for _, exchange := range []string{"SH", "SZ"} {
			if err := collect("etf_basic", etfFields, exchange, 5000, etfInstrument); err != nil {
				return nil, err
			}
		}
	}
	p.etfMu.Lock()
	for _, it := range result {
		if it.AssetType == domain.AssetETF {
			p.verifiedETFs[it.Code] = true
		}
	}
	p.etfMu.Unlock()
	sort.Slice(result, func(i, j int) bool { return result[i].Code < result[j].Code })
	return result, nil
}

func (p *tushareProvider) verifyETF(ctx context.Context, code string) error {
	p.etfMu.RLock()
	verified := p.verifiedETFs[code]
	p.etfMu.RUnlock()
	if verified {
		return nil
	}
	rows, err := p.call(ctx, "etf_basic", etfFields, map[string]any{"ts_code": tsCode(code), "list_status": "L"})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return datasource.ErrProviderResponse
	}
	it, err := etfInstrument(rows[0])
	if err != nil {
		return err
	}
	if it.Code != code {
		return datasource.ErrProviderResponse
	}
	p.etfMu.Lock()
	p.verifiedETFs[code] = true
	p.etfMu.Unlock()
	return nil
}

// FetchBars is the stock contract. Call FetchInstrumentBars for a persisted ETF
// identity; neither names nor ticker prefixes are used to guess asset type.
func (p *tushareProvider) FetchBars(ctx context.Context, code string, period domain.BarPeriod, start, end time.Time) ([]*domain.Bar, error) {
	return p.FetchInstrumentBars(ctx, &domain.Instrument{Code: code, AssetType: domain.AssetStock}, period, start, end)
}

func (p *tushareProvider) FetchInstrumentBars(ctx context.Context, it *domain.Instrument, period domain.BarPeriod, start, end time.Time) ([]*domain.Bar, error) {
	if it == nil || it.AssetType != domain.AssetStock && it.AssetType != domain.AssetETF {
		return nil, datasource.ErrProviderInput
	}
	start, end, err := dateRange(it.Code, period, start, end)
	if err != nil {
		return nil, err
	}
	api := map[domain.BarPeriod]string{domain.Bar1d: "daily", domain.Bar1w: "weekly", domain.Bar1mo: "monthly"}[period]
	cap := 6000
	if period == domain.Bar1mo {
		cap = 4500
	}
	if it.AssetType == domain.AssetETF {
		if !p.includeETF || period != domain.Bar1d {
			return nil, datasource.ErrProviderUnsupported
		}
		if err := p.verifyETF(ctx, it.Code); err != nil {
			return nil, err
		}
		api, cap = "fund_daily", 5000
	}
	result := make([]*domain.Bar, 0)
	seen := make(map[time.Time]bool)
	// Official history endpoints accept a date range. Annual windows fit well
	// below every published cap and avoid relying on undocumented offset args.
	for from := start; !from.After(end); {
		to := from.AddDate(1, 0, 0).AddDate(0, 0, -1)
		if to.After(end) {
			to = end
		}
		rows, err := p.call(ctx, api, barFields, map[string]any{"ts_code": tsCode(it.Code), "start_date": from.Format("20060102"), "end_date": to.Format("20060102")})
		if err != nil {
			return nil, err
		}
		if len(rows) >= cap || len(rows) > int(to.Sub(from).Hours()/24)+1 {
			return nil, datasource.ErrProviderPagination
		}
		for _, row := range rows {
			bar, err := parseTSBar(row, it.Code, period, from, to, api)
			if err != nil {
				return nil, err
			}
			if seen[bar.Date] {
				return nil, datasource.ErrProviderPagination
			}
			seen[bar.Date] = true
			result = append(result, bar)
		}
		from = to.AddDate(0, 0, 1)
	}
	if it.AssetType == domain.AssetETF && len(result) > 0 {
		factors, err := p.factors(ctx, it.Code, start, end)
		if err != nil {
			return nil, err
		}
		for _, bar := range result {
			factor, ok := factors[bar.Date]
			if !ok {
				return nil, datasource.ErrProviderResponse
			}
			bar.AdjFactor, bar.AdjSource = factor, domain.AdjFactorTushareFund
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Date.Before(result[j].Date) })
	return result, nil
}

func parseTSBar(row tsRow, code string, period domain.BarPeriod, start, end time.Time, api string) (*domain.Bar, error) {
	actualCode, err := stringValue(row, "ts_code", false)
	if err != nil {
		return nil, err
	}
	actualCode, err = canonicalTSCode(actualCode)
	if err != nil {
		return nil, err
	}
	dateText, err := stringValue(row, "trade_date", false)
	if err != nil {
		return nil, err
	}
	date, err := parseDate(dateText, "20060102")
	if err != nil {
		return nil, err
	}
	bar := &domain.Bar{Code: actualCode, Period: period, Date: date, AdjFactor: 1, AdjSource: "tushare." + api}
	for _, field := range []struct {
		key    string
		target *float64
	}{{"open", &bar.Open}, {"high", &bar.High}, {"low", &bar.Low}, {"close", &bar.Close}, {"vol", &bar.Volume}, {"amount", &bar.Amount}} {
		*field.target, err = numberValue(row, field.key)
		if err != nil {
			return nil, err
		}
	}
	// Tushare's quote volume is lots (100 shares), turnover is thousands of
	// yuan. The repository/HTTP contract uses shares and yuan.
	bar.Volume *= 100
	bar.Amount *= 1000
	return bar, validateBar(bar, code, period, start, end)
}

func (p *tushareProvider) factors(ctx context.Context, code string, start, end time.Time) (map[time.Time]float64, error) {
	result := make(map[time.Time]float64)
	for page := 0; page < maxPages; page++ {
		rows, err := p.call(ctx, "fund_adj", factorFields, map[string]any{"ts_code": tsCode(code), "start_date": start.Format("20060102"), "end_date": end.Format("20060102"), "limit": pageSize, "offset": page * pageSize})
		if err != nil {
			return nil, err
		}
		if len(rows) > pageSize {
			return nil, datasource.ErrProviderPagination
		}
		for _, row := range rows {
			actual, err := stringValue(row, "ts_code", false)
			if err != nil {
				return nil, err
			}
			actual, err = canonicalTSCode(actual)
			if err != nil || actual != code {
				return nil, datasource.ErrProviderResponse
			}
			text, err := stringValue(row, "trade_date", false)
			if err != nil {
				return nil, err
			}
			date, err := parseDate(text, "20060102")
			if err != nil || date.Before(start) || date.After(end) {
				return nil, datasource.ErrProviderResponse
			}
			factor, err := numberValue(row, "adj_factor")
			if err != nil || factor <= 0 {
				return nil, datasource.ErrProviderResponse
			}
			if _, exists := result[date]; exists {
				return nil, datasource.ErrProviderPagination
			}
			result[date] = factor
		}
		if len(result) > maxRows {
			return nil, datasource.ErrProviderPagination
		}
		if len(rows) < pageSize {
			return result, nil
		}
	}
	return nil, datasource.ErrProviderPagination
}

var _ datasource.Client = (*tushareProvider)(nil)
var _ datasource.Prober = (*tushareProvider)(nil)
var _ datasource.RequestLimitedClient = (*tushareProvider)(nil)
var _ datasource.InstrumentBarsClient = (*tushareProvider)(nil)
