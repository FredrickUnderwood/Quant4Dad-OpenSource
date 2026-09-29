package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository/datasource"
)

func tsTestClient(t *testing.T, handler func(http.ResponseWriter, tsRequest)) *tushareProvider {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request tsRequest
		if r.Method != "POST" || r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" || sonic.ConfigStd.NewDecoder(r.Body).Decode(&request) != nil || request.Token != "test-official-token" {
			t.Error("invalid official request")
			w.WriteHeader(400)
			return
		}
		handler(w, request)
	}))
	t.Cleanup(server.Close)
	client, err := newTushareWithEndpoint(config.DatasourceConfig{Token: "test-official-token", IncludeETF: true}, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func tsWriteRows(t *testing.T, w http.ResponseWriter, request tsRequest, rows ...tsRow) {
	t.Helper()
	fields := strings.Split(request.Fields, ",")
	items := make([][]any, 0, len(rows))
	for _, row := range rows {
		values := make([]any, len(fields))
		for i, field := range fields {
			values[i] = row[field]
		}
		items = append(items, values)
	}
	writeJSON(t, w, map[string]any{"code": 0, "msg": "", "request_id": "synthetic", "data": map[string]any{"fields": fields, "items": items}})
}

func stockRow(code, exchange string) tsRow {
	return tsRow{"ts_code": code, "name": "Synthetic stock", "industry": nil, "exchange": exchange, "list_status": "L", "list_date": "20200102"}
}
func etfRow(code, exchange string) tsRow {
	return tsRow{"ts_code": code, "csname": "Synthetic ETF", "extname": "Synthetic ETF extended", "cname": "Synthetic exchange traded fund", "index_code": "000300.SH", "index_name": "Synthetic index", "setup_date": "20190102", "list_date": "20200102", "list_status": "L", "exchange": exchange, "mgr_name": "Synthetic manager", "custod_name": "Synthetic custodian", "mgt_fee": 0.2, "etf_type": "境内"}
}
func tsBar(code, day string) tsRow {
	return tsRow{"ts_code": code, "trade_date": day, "open": 10, "high": 12, "low": 9, "close": 11, "vol": 3, "amount": 4}
}
func factorRow(code, day string, factor any) tsRow {
	return tsRow{"ts_code": code, "trade_date": day, "adj_factor": factor}
}

func TestTushareCatalogIdentityAndPerRequestQuota(t *testing.T) {
	var requests, gates atomic.Int32
	client := tsTestClient(t, func(w http.ResponseWriter, r tsRequest) {
		requests.Add(1)
		if r.Params["list_status"] != "L" {
			t.Error("catalog must request listed instruments")
		}
		if r.APIName == "stock_basic" {
			switch r.Params["exchange"] {
			case "SSE":
				tsWriteRows(t, w, r, stockRow("600000.SH", "SSE"))
			case "SZSE":
				tsWriteRows(t, w, r, stockRow("000001.SZ", "SZSE"))
			case "BSE":
				tsWriteRows(t, w, r, stockRow("430001.BJ", "BSE"))
			default:
				t.Error("unpartitioned stock catalog")
			}
		} else if r.APIName == "etf_basic" {
			switch r.Params["exchange"] {
			case "SH":
				tsWriteRows(t, w, r, etfRow("510300.SH", "SH"))
			case "SZ":
				tsWriteRows(t, w, r, etfRow("159999.SZ", "SZ"))
			default:
				t.Error("unpartitioned ETF catalog")
			}
		} else {
			t.Error("wrong catalog API")
		}
	})
	client.SetRequestLimiter(func(context.Context) error { gates.Add(1); return nil })
	items, err := client.ListInstruments(context.Background())
	if err != nil || len(items) != 5 {
		t.Fatalf("catalog: %v", err)
	}
	if items[0].Code != "bj.430001" || items[1].AssetType != domain.AssetETF || items[1].Name != "Synthetic ETF extended" || items[1].ManagementFee == nil || *items[1].ManagementFee != 0.2 {
		t.Fatal("metadata conversion")
	}
	if requests.Load() != 5 || gates.Load() != 5 {
		t.Fatal("one limiter call per upstream request required")
	}
}

func TestTushareStockOnlyAndProbeBounded(t *testing.T) {
	calls := 0
	client := tsTestClient(t, func(w http.ResponseWriter, r tsRequest) {
		calls++
		if r.APIName != "stock_basic" {
			t.Error("unexpected ETF/market fetch")
		}
		if r.Params["ts_code"] == "000001.SZ" {
			tsWriteRows(t, w, r, stockRow("000001.SZ", "SZSE"))
		} else {
			tsWriteRows(t, w, r)
		}
	})
	client.includeETF = false
	items, err := client.ListInstruments(context.Background())
	if err != nil || len(items) != 0 || calls != 3 {
		t.Fatal("stock-only catalog did not stay bounded")
	}
	if err := client.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatal("probe was not a single read")
	}
}

func TestTushareStockPeriodsAnnualWindowsAndRawUnits(t *testing.T) {
	for period, api := range map[domain.BarPeriod]string{domain.Bar1d: "daily", domain.Bar1w: "weekly", domain.Bar1mo: "monthly"} {
		t.Run(string(period), func(t *testing.T) {
			calls, gates := 0, 0
			client := tsTestClient(t, func(w http.ResponseWriter, r tsRequest) {
				calls++
				if r.APIName != api || r.Params["ts_code"] != "600000.SH" || r.Fields != barFields {
					t.Error("wrong raw history endpoint")
				}
				from := r.Params["start_date"].(string)
				to := r.Params["end_date"].(string)
				if from != "20230101" && from != "20240101" && from != "20250101" {
					t.Error("windows overlap or skip")
				}
				if from == "20250101" && to != "20250102" {
					t.Error("last window overshot request")
				}
				tsWriteRows(t, w, r, tsBar("600000.SH", to), tsBar("600000.SH", from))
			})
			client.SetRequestLimiter(func(context.Context) error { gates++; return nil })
			bars, err := client.FetchBars(context.Background(), "sh.600000", period, date("2023-01-01"), date("2025-01-02"))
			if err != nil || len(bars) != 6 || calls != 3 || gates != 3 {
				t.Fatalf("history: bars=%d calls=%d gates=%d err=%v", len(bars), calls, gates, err)
			}
			for i, bar := range bars {
				if bar.Volume != 300 || bar.Amount != 4000 || bar.AdjFactor != 1 || bar.AdjSource == domain.AdjFactorTushareFund || bar.Period != period || i > 0 && !bars[i-1].Date.Before(bar.Date) {
					t.Fatal("raw units/order/provenance")
				}
			}
		})
	}
}

func TestTushareETFVerifiedDailyFactorJoin(t *testing.T) {
	calls := []string{}
	client := tsTestClient(t, func(w http.ResponseWriter, r tsRequest) {
		calls = append(calls, r.APIName)
		switch r.APIName {
		case "etf_basic":
			if r.Params["ts_code"] != "510300.SH" {
				t.Error("ETF identity must be verified")
			}
			tsWriteRows(t, w, r, etfRow("510300.SH", "SH"))
		case "fund_daily":
			tsWriteRows(t, w, r, tsBar("510300.SH", "20240103"), tsBar("510300.SH", "20240102"))
		case "fund_adj":
			tsWriteRows(t, w, r, factorRow("510300.SH", "20240102", 1.0), factorRow("510300.SH", "20240103", 1.25))
		default:
			t.Error("wrong ETF routing")
		}
	})
	it := &domain.Instrument{Code: "sh.510300", AssetType: domain.AssetETF}
	bars, err := client.FetchInstrumentBars(context.Background(), it, domain.Bar1d, date("2024-01-02"), date("2024-01-03"))
	if err != nil || len(bars) != 2 {
		t.Fatalf("ETF fetch: %v", err)
	}
	if strings.Join(calls, ",") != "etf_basic,fund_daily,fund_adj" || bars[0].AdjFactor != 1 || bars[1].AdjFactor != 1.25 || bars[0].AdjSource != domain.AdjFactorTushareFund || bars[1].AdjSource != domain.AdjFactorTushareFund {
		t.Fatal("ETF factor join")
	}
	for _, period := range []domain.BarPeriod{domain.Bar1w, domain.Bar1mo} {
		if _, err := client.FetchInstrumentBars(context.Background(), it, period, date("2024-01-02"), date("2024-01-03")); !errors.Is(err, datasource.ErrProviderUnsupported) {
			t.Fatal("ETF weekly/monthly accepted")
		}
	}
	if len(calls) != 3 {
		t.Fatal("unsupported periods made HTTP requests")
	}
}

func TestTushareFactorPaginationAndRepeatedOffset(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		t.Run(strconv.FormatBool(duplicate), func(t *testing.T) {
			calls, gates := 0, 0
			start := date("2020-01-01")
			client := tsTestClient(t, func(w http.ResponseWriter, r tsRequest) {
				calls++
				if r.APIName != "fund_adj" || r.Params["limit"] != float64(pageSize) {
					t.Error("factor pagination contract")
				}
				offset := int(r.Params["offset"].(float64))
				if offset != 0 && offset != 1000 {
					t.Error("bad offset")
				}
				if duplicate {
					offset = 0
				}
				count := pageSize
				if offset > 0 {
					count = 1
				}
				rows := make([]tsRow, 0, count)
				for i := 0; i < count; i++ {
					rows = append(rows, factorRow("510300.SH", start.AddDate(0, 0, offset+i).Format("20060102"), 1.0))
				}
				tsWriteRows(t, w, r, rows...)
			})
			client.SetRequestLimiter(func(context.Context) error { gates++; return nil })
			factors, err := client.factors(context.Background(), "sh.510300", start, date("2023-01-01"))
			if duplicate {
				if !errors.Is(err, datasource.ErrProviderPagination) || factors != nil {
					t.Fatal("ignored offset accepted")
				}
			} else if err != nil || len(factors) != 1001 {
				t.Fatalf("pagination: %v", err)
			}
			if calls != 2 || gates != 2 {
				t.Fatal("pagination quota")
			}
		})
	}
}

func TestTushareFactorBoundariesFailClosed(t *testing.T) {
	cases := map[string][]tsRow{
		"missing":        {},
		"zero":           {factorRow("510300.SH", "20240102", 0)},
		"negative":       {factorRow("510300.SH", "20240102", -1)},
		"null":           {factorRow("510300.SH", "20240102", nil)},
		"numeric string": {factorRow("510300.SH", "20240102", "1.1")},
		"wrong code":     {factorRow("159999.SZ", "20240102", 1)},
		"out of range":   {factorRow("510300.SH", "20240101", 1)},
		"invalid date":   {factorRow("510300.SH", "20240230", 1)},
		"duplicate":      {factorRow("510300.SH", "20240102", 1), factorRow("510300.SH", "20240102", 1)},
	}
	for name, rows := range cases {
		t.Run(name, func(t *testing.T) {
			client := tsTestClient(t, func(w http.ResponseWriter, r tsRequest) {
				switch r.APIName {
				case "etf_basic":
					tsWriteRows(t, w, r, etfRow("510300.SH", "SH"))
				case "fund_daily":
					tsWriteRows(t, w, r, tsBar("510300.SH", "20240102"))
				case "fund_adj":
					tsWriteRows(t, w, r, rows...)
				}
			})
			bars, err := client.FetchInstrumentBars(context.Background(), &domain.Instrument{Code: "sh.510300", AssetType: domain.AssetETF}, domain.Bar1d, date("2024-01-02"), date("2024-01-03"))
			if err == nil || bars != nil {
				t.Fatal("partial/unverified factors accepted")
			}
		})
	}
}

func TestTushareRejectsFalseETFIdentity(t *testing.T) {
	calls := 0
	client := tsTestClient(t, func(w http.ResponseWriter, r tsRequest) {
		calls++
		if r.APIName != "etf_basic" {
			t.Error("unverified ETF fetched history")
		}
		tsWriteRows(t, w, r)
	})
	bars, err := client.FetchInstrumentBars(context.Background(), &domain.Instrument{Code: "sh.600000", AssetType: domain.AssetETF}, domain.Bar1d, date("2024-01-02"), date("2024-01-03"))
	if !errors.Is(err, datasource.ErrProviderResponse) || bars != nil || calls != 1 {
		t.Fatal("forged ETF identity accepted")
	}
}

func TestTushareCatalogCapAndInvalidResponse(t *testing.T) {
	for _, mode := range []string{"stock cap", "ETF cap", "duplicate fields", "missing field", "row width", "wrong identity", "duplicate rows"} {
		t.Run(mode, func(t *testing.T) {
			client := tsTestClient(t, func(w http.ResponseWriter, r tsRequest) {
				switch mode {
				case "stock cap":
					rows := make([]tsRow, 6000)
					for i := range rows {
						rows[i] = stockRow("600000.SH", "SSE")
					}
					tsWriteRows(t, w, r, rows...)
				case "ETF cap":
					if r.APIName == "stock_basic" {
						tsWriteRows(t, w, r)
					} else {
						rows := make([]tsRow, 5000)
						for i := range rows {
							rows[i] = etfRow("510300.SH", "SH")
						}
						tsWriteRows(t, w, r, rows...)
					}
				case "duplicate fields":
					writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"fields": []string{"ts_code", "ts_code"}, "items": [][]any{}}})
				case "missing field":
					writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"fields": []string{"ts_code"}, "items": [][]any{}}})
				case "row width":
					writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"fields": strings.Split(stockFields, ","), "items": [][]any{{"600000.SH"}}}})
				case "wrong identity":
					tsWriteRows(t, w, r, stockRow("000001.SZ", "SSE"))
				case "duplicate rows":
					tsWriteRows(t, w, r, stockRow("600000.SH", "SSE"), stockRow("600000.SH", "SSE"))
				}
			})
			items, err := client.ListInstruments(context.Background())
			if err == nil || items != nil {
				t.Fatal("invalid catalog accepted")
			}
		})
	}
}

func TestTushareTokenCannotBeReroutedAndErrorsAreSanitized(t *testing.T) {
	for _, base := range []string{"http://api.tushare.pro", "https://api.tushare.pro.evil.test", "https://other.test", "https://api.tushare.pro/path", "https://api.tushare.pro?token=x"} {
		if _, err := newTushare(config.DatasourceConfig{Token: "test-official-token", BaseURL: base}); !errors.Is(err, datasource.ErrProviderConfig) {
			t.Fatal("official credential can be rerouted")
		}
	}
	if _, err := newTushare(config.DatasourceConfig{}); !errors.Is(err, datasource.ErrProviderConfig) {
		t.Fatal("empty official token accepted")
	}
	client, err := newTushare(config.DatasourceConfig{Token: "test-official-token"})
	if err != nil || client.(*tushareProvider).endpoint != tushareEndpoint {
		t.Fatal("official HTTPS endpoint changed")
	}
	for _, code := range []int{2002, 5000} {
		client := tsTestClient(t, func(w http.ResponseWriter, r tsRequest) {
			writeJSON(t, w, map[string]any{"code": code, "msg": "test-official-token upstream-private-message", "data": nil})
		})
		err := client.Probe(context.Background())
		want := datasource.ErrProviderUpstream
		if code == 2002 {
			want = datasource.ErrProviderAccess
		}
		var fetchErr *datasource.FetchError
		if !errors.Is(err, want) || !errors.As(err, &fetchErr) || fetchErr.Body != "" || strings.Contains(err.Error(), "test-official-token") || strings.Contains(err.Error(), "upstream-private-message") {
			t.Fatal("official API error leaked or misclassified")
		}
	}
}

func TestProvidersRejectInvalidInputWithoutNetwork(t *testing.T) {
	client := tsTestClient(t, func(w http.ResponseWriter, r tsRequest) { t.Error("invalid input made request") })
	for _, test := range []struct {
		code       string
		period     domain.BarPeriod
		start, end time.Time
	}{
		{"bad", domain.Bar1d, date("2024-01-01"), date("2024-01-02")},
		{"sh.600000", "invalid", date("2024-01-01"), date("2024-01-02")},
		{"sh.600000", domain.Bar1d, time.Time{}, date("2024-01-02")},
		{"sh.600000", domain.Bar1d, date("2024-01-02"), date("2024-01-01")},
	} {
		if _, err := client.FetchBars(context.Background(), test.code, test.period, test.start, test.end); !errors.Is(err, datasource.ErrProviderInput) {
			t.Fatal("invalid input accepted")
		}
	}
}

func TestTushareInvalidHistoryCannotBecomePartialSuccess(t *testing.T) {
	cases := map[string]func(tsRow){
		"wrong code":      func(r tsRow) { r["ts_code"] = "000001.SZ" },
		"wrong date":      func(r tsRow) { r["trade_date"] = "20240104" },
		"negative price":  func(r tsRow) { r["open"] = -1 },
		"bad range":       func(r tsRow) { r["high"] = 10 },
		"negative volume": func(r tsRow) { r["vol"] = -1 },
		"overflow units":  func(r tsRow) { r["amount"] = 1e308 },
		"null value":      func(r tsRow) { r["close"] = nil },
		"numeric string":  func(r tsRow) { r["close"] = "11" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			client := tsTestClient(t, func(w http.ResponseWriter, r tsRequest) {
				bad := tsBar("600000.SH", "20240103")
				mutate(bad)
				tsWriteRows(t, w, r, tsBar("600000.SH", "20240102"), bad)
			})
			bars, err := client.FetchBars(context.Background(), "sh.600000", domain.Bar1d, date("2024-01-02"), date("2024-01-03"))
			if !errors.Is(err, datasource.ErrProviderResponse) || bars != nil {
				t.Fatalf("invalid raw history accepted: %v", err)
			}
		})
	}
}
