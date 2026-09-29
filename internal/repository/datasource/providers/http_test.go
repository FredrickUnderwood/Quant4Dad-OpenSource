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

func date(value string) time.Time {
	parsed, err := time.Parse(time.DateOnly, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	body, err := sonic.Marshal(value)
	if err != nil {
		t.Error(err)
		w.WriteHeader(500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func httpTestClient(t *testing.T, handler http.HandlerFunc) *httpProvider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := newHTTPWithClient(config.DatasourceConfig{BaseURL: server.URL + "/data", Token: "test-private-token", IncludeETF: true}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func instrumentJSON(code, asset string) map[string]any {
	return map[string]any{"code": code, "name": "Synthetic instrument", "asset_type": asset, "listed_date": "2020-01-02"}
}

func barJSON(day string) map[string]any {
	return map[string]any{"code": "sh.600000", "period": "1d", "date": day, "open": 10, "high": 12, "low": 9, "close": 11, "volume": 100, "amount": 1100}
}

func TestHTTPPaginationAndNormalization(t *testing.T) {
	var calls, gates atomic.Int32
	client := httpTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer test-private-token" || r.URL.Query().Get("limit") != "1000" {
			t.Error("invalid request contract")
		}
		if strings.Contains(r.URL.String(), "test-private-token") {
			t.Error("credential in URL")
		}
		switch r.URL.Path {
		case "/data/instruments":
			if r.URL.Query().Get("cursor") == "" {
				writeJSON(t, w, map[string]any{"items": []any{instrumentJSON("sz.000001", "stock")}, "next_cursor": "a&b / opaque"})
			} else {
				if r.URL.Query().Get("cursor") != "a&b / opaque" {
					t.Error("cursor was changed")
				}
				writeJSON(t, w, map[string]any{"items": []any{instrumentJSON("sh.510300", "etf")}, "next_cursor": ""})
			}
		case "/data/bars":
			q := r.URL.Query()
			if q.Get("code") != "sh.600000" || q.Get("period") != "1d" || q.Get("start") != "2024-01-02" || q.Get("end") != "2024-01-03" {
				t.Error("invalid bar query")
			}
			if q.Get("cursor") == "" {
				b := barJSON("2024-01-03")
				b["adj_factor"] = 2
				writeJSON(t, w, map[string]any{"items": []any{b}, "next_cursor": "second"})
			} else {
				writeJSON(t, w, map[string]any{"items": []any{barJSON("2024-01-02")}, "next_cursor": ""})
			}
		default:
			t.Error("unexpected endpoint")
			w.WriteHeader(404)
		}
	})
	client.SetRequestLimiter(func(context.Context) error { gates.Add(1); return nil })
	instruments, err := client.ListInstruments(context.Background())
	if err != nil || len(instruments) != 2 {
		t.Fatalf("catalog: %v", err)
	}
	if instruments[0].Code != "sh.510300" || instruments[0].AssetType != domain.AssetETF || instruments[0].Exchange != "SH" || instruments[0].Status != "active" {
		t.Fatal("instrument mapping")
	}
	bars, err := client.FetchInstrumentBars(context.Background(), &domain.Instrument{Code: "sh.600000", AssetType: domain.AssetStock}, domain.Bar1d, date("2024-01-02"), date("2024-01-03"))
	if err != nil || len(bars) != 2 {
		t.Fatalf("bars: %v", err)
	}
	if !bars[0].Date.Before(bars[1].Date) || bars[0].AdjFactor != 1 || bars[1].AdjFactor != 2 || bars[1].Volume != 100 || bars[1].Amount != 1100 || bars[1].AdjSource != "http" {
		t.Fatal("bar normalization/provenance")
	}
	if calls.Load() != 4 || gates.Load() != 4 {
		t.Fatal("quota must apply to every HTTP page")
	}
}

func TestHTTPProbeOnlyFirstPage(t *testing.T) {
	var calls int
	client := httpTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeJSON(t, w, map[string]any{"items": []any{instrumentJSON("sh.600000", "stock")}, "next_cursor": "remaining"})
	})
	if err := client.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("probe harvested more than one page")
	}
}

func TestHTTPRejectsInvalidInstrumentPages(t *testing.T) {
	cases := map[string]string{
		"missing items":      `{"next_cursor":""}`,
		"null items":         `{"items":null,"next_cursor":""}`,
		"missing terminator": `{"items":[]}`,
		"empty continuation": `{"items":[],"next_cursor":"loop"}`,
		"bad code":           `{"items":[{"code":"600000.SH","name":"x","asset_type":"stock"}],"next_cursor":""}`,
		"missing asset":      `{"items":[{"code":"sh.600000","name":"x"}],"next_cursor":""}`,
		"wrong exchange":     `{"items":[{"code":"sh.600000","name":"x","asset_type":"stock","exchange":"SZ"}],"next_cursor":""}`,
		"bad date":           `{"items":[{"code":"sh.600000","name":"x","asset_type":"stock","listed_date":"2024-02-30"}],"next_cursor":""}`,
		"bad status":         `{"items":[{"code":"sh.600000","name":"x","asset_type":"stock","status":"oops"}],"next_cursor":""}`,
		"wrong type":         `{"items":[{"code":"sh.600000","name":1,"asset_type":"stock"}],"next_cursor":""}`,
		"negative fee":       `{"items":[{"code":"sh.600000","name":"x","asset_type":"stock","management_fee":-1}],"next_cursor":""}`,
		"unknown property":   `{"items":[{"code":"sh.600000","name":"x","asset_type":"stock","credential":"echoed"}],"next_cursor":""}`,
		"trailing JSON":      `{"items":[],"next_cursor":""}{"extra":true}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			client := httpTestClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
			items, err := client.ListInstruments(context.Background())
			if err == nil || items != nil {
				t.Fatal("invalid/partial catalog accepted")
			}
		})
	}
}

func TestHTTPRejectsInvalidBarsAndForgedProvenance(t *testing.T) {
	cases := map[string]func(map[string]any){
		"wrong code":      func(b map[string]any) { b["code"] = "sz.000001" },
		"wrong period":    func(b map[string]any) { b["period"] = "1w" },
		"outside date":    func(b map[string]any) { b["date"] = "2024-01-04" },
		"missing price":   func(b map[string]any) { delete(b, "open") },
		"null price":      func(b map[string]any) { b["open"] = nil },
		"zero price":      func(b map[string]any) { b["close"] = 0 },
		"price range":     func(b map[string]any) { b["high"] = 10 },
		"negative volume": func(b map[string]any) { b["volume"] = -1 },
		"negative amount": func(b map[string]any) { b["amount"] = -1 },
		"zero factor":     func(b map[string]any) { b["adj_factor"] = 0 },
		"forged source":   func(b map[string]any) { b["adj_source"] = domain.AdjFactorTushareFund },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			b := barJSON("2024-01-02")
			mutate(b)
			client := httpTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(t, w, map[string]any{"items": []any{b}, "next_cursor": ""})
			})
			bars, err := client.FetchBars(context.Background(), "sh.600000", domain.Bar1d, date("2024-01-02"), date("2024-01-03"))
			if !errors.Is(err, datasource.ErrProviderResponse) || bars != nil {
				t.Fatalf("invalid bars accepted: %v", err)
			}
		})
	}
}

func TestHTTPPaginationFailuresDiscardPartialResults(t *testing.T) {
	for _, mode := range []string{"cursor cycle", "duplicate instrument", "duplicate bar", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			count := 0
			client := httpTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				count++
				items := []any{instrumentJSON("sh.600000", "stock")}
				next := "same"
				switch mode {
				case "cursor cycle":
					if count > 1 {
						items = []any{instrumentJSON("sz.000001", "stock")}
					}
				case "duplicate instrument":
					if count > 1 {
						next = ""
					}
				case "duplicate bar":
					items = []any{barJSON("2024-01-02")}
					if count > 1 {
						next = ""
					}
				case "oversized":
					items = make([]any, pageSize+1)
					for i := range items {
						items[i] = instrumentJSON("sh.600000", "stock")
					}
					next = ""
				}
				writeJSON(t, w, map[string]any{"items": items, "next_cursor": next})
			})
			if mode == "duplicate bar" {
				items, err := client.FetchBars(context.Background(), "sh.600000", domain.Bar1d, date("2024-01-02"), date("2024-01-03"))
				if err == nil || items != nil {
					t.Fatal("partial bars accepted")
				}
			} else {
				items, err := client.ListInstruments(context.Background())
				if err == nil || items != nil {
					t.Fatal("partial catalog accepted")
				}
			}
			if count > 2 {
				t.Fatal("pagination loop was not stopped")
			}
		})
	}
}

func TestHTTPRejectsUnsafeConfiguration(t *testing.T) {
	for _, base := range []string{"", "ftp://example.test", "https://user:password@example.test", "https://example.test?x=y", "https://example.test?", "https://example.test/#fragment", "https://example.test/#", "relative/path"} {
		if _, err := newHTTP(config.DatasourceConfig{BaseURL: base}); !errors.Is(err, datasource.ErrProviderConfig) {
			t.Fatal("unsafe URL accepted")
		}
	}
	for _, token := range []string{" leading", "trailing ", "bad\nheader", strings.Repeat("x", 4097)} {
		if _, err := newHTTP(config.DatasourceConfig{BaseURL: "https://example.test", Token: token}); !errors.Is(err, datasource.ErrProviderConfig) {
			t.Fatal("invalid token accepted")
		}
	}
}

func TestHTTPNoRedirectOrErrorBodyLeak(t *testing.T) {
	var received atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer other.Close()
	for _, status := range []int{301, 302, 307, 308, 401, 403, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			client := httpTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", other.URL)
				w.WriteHeader(status)
				_, _ = w.Write([]byte("test-private-token upstream-secret-body"))
			})
			err := client.Probe(context.Background())
			var fetchErr *datasource.FetchError
			if err == nil || strings.Contains(err.Error(), "test-private-token") || strings.Contains(err.Error(), "upstream-secret-body") || !errors.As(err, &fetchErr) || fetchErr.Body != "" {
				t.Fatal("error is not sanitized")
			}
			want := datasource.ErrProviderUpstream
			if status == 401 || status == 403 {
				want = datasource.ErrProviderAccess
			}
			if status == 429 {
				want = datasource.ErrProviderRateLimit
			}
			if !errors.Is(err, want) {
				t.Fatalf("wrong classification: %v", err)
			}
		})
	}
	if received.Load() != 0 {
		t.Fatal("redirect leaked request")
	}
}

func TestHTTPTimeoutAndBoundedBody(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		client := httpTestClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if err := client.Probe(ctx); !errors.Is(err, datasource.ErrProviderTimeout) {
			t.Fatalf("timeout: %v", err)
		}
	})
	t.Run("body limit", func(t *testing.T) {
		client := httpTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat(" ", maxResponseBytes+1)))
		})
		if err := client.Probe(context.Background()); !errors.Is(err, datasource.ErrProviderResponse) {
			t.Fatalf("limit: %v", err)
		}
	})
	t.Run("canceled quota", func(t *testing.T) {
		client := httpTestClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("request must not be sent") })
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := client.Probe(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}

func TestHTTPExplicitTerminationAndGlobalPageLimits(t *testing.T) {
	calls := 0
	if err := walkPages(func(cursor string) (string, int, error) { calls++; return "", pageSize, nil }); err != nil || calls != 1 {
		t.Fatal("full final page must honor explicit termination")
	}
	calls = 0
	if err := walkPages(func(cursor string) (string, int, error) { calls++; return strconv.Itoa(calls), 1, nil }); !errors.Is(err, datasource.ErrProviderPagination) || calls != maxPages {
		t.Fatal("page count must be bounded")
	}
	calls = 0
	if err := walkPages(func(cursor string) (string, int, error) { calls++; return strconv.Itoa(calls), pageSize, nil }); !errors.Is(err, datasource.ErrProviderPagination) || calls != maxRows/pageSize+1 {
		t.Fatal("total records must be bounded")
	}
}

func TestHTTPOptionalAuthenticationAndNonFiniteValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("unconfigured credential sent")
		}
		_, _ = w.Write([]byte(`{"items":[{"code":"sh.600000","period":"1d","date":"2024-01-02","open":10,"high":12,"low":9,"close":11,"volume":1e400,"amount":1100}],"next_cursor":""}`))
	}))
	defer server.Close()
	client, err := newHTTP(config.DatasourceConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.FetchBars(context.Background(), "sh.600000", domain.Bar1d, date("2024-01-02"), date("2024-01-03")); !errors.Is(err, datasource.ErrProviderResponse) {
		t.Fatal("non-finite value accepted")
	}
}

func TestHTTPIncludeETFFiltersCatalogAndGatesTypedFetch(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(strconv.FormatBool(enabled), func(t *testing.T) {
			catalogCalls, barCalls := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/instruments":
					catalogCalls++
					if r.URL.Query().Get("cursor") == "" {
						writeJSON(t, w, map[string]any{"items": []any{instrumentJSON("sh.510300", "etf")}, "next_cursor": "stock-page"})
					} else {
						writeJSON(t, w, map[string]any{"items": []any{instrumentJSON("sh.600000", "stock")}, "next_cursor": ""})
					}
				case "/bars":
					barCalls++
					bar := barJSON("2024-01-02")
					bar["code"] = "sh.510300"
					writeJSON(t, w, map[string]any{"items": []any{bar}, "next_cursor": ""})
				default:
					t.Error("unexpected endpoint")
				}
			}))
			defer server.Close()
			client, err := newHTTP(config.DatasourceConfig{BaseURL: server.URL, IncludeETF: enabled})
			if err != nil {
				t.Fatal(err)
			}
			items, err := client.ListInstruments(context.Background())
			wantCount := 1
			if enabled {
				wantCount = 2
			}
			if err != nil || len(items) != wantCount || catalogCalls != 2 {
				t.Fatalf("catalog filtering/pagination: rows=%d calls=%d err=%v", len(items), catalogCalls, err)
			}
			if !enabled && items[0].AssetType != domain.AssetStock {
				t.Fatal("ETF escaped disabled filter")
			}
			typed := client.(datasource.InstrumentBarsClient)
			it := &domain.Instrument{Code: "sh.510300", AssetType: domain.AssetETF}
			bars, err := typed.FetchInstrumentBars(context.Background(), it, domain.Bar1d, date("2024-01-02"), date("2024-01-03"))
			if enabled {
				if err != nil || len(bars) != 1 || bars[0].AdjSource != "http" || barCalls != 1 {
					t.Fatal("enabled ETF daily fetch did not use untrusted HTTP contract")
				}
			} else if !errors.Is(err, datasource.ErrProviderUnsupported) || bars != nil || barCalls != 0 {
				t.Fatal("disabled ETF fetch sent request")
			}
			before := barCalls
			for _, period := range []domain.BarPeriod{domain.Bar1w, domain.Bar1mo} {
				if _, err := typed.FetchInstrumentBars(context.Background(), it, period, date("2024-01-02"), date("2024-01-03")); !errors.Is(err, datasource.ErrProviderUnsupported) {
					t.Fatal("ETF non-daily fetch accepted")
				}
			}
			if barCalls != before {
				t.Fatal("unsupported ETF period sent request")
			}
		})
	}
}

func TestHTTPDisabledETFStillValidatesCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bad := instrumentJSON("sh.510300", "etf")
		bad["listed_date"] = "invalid"
		writeJSON(t, w, map[string]any{"items": []any{instrumentJSON("sh.600000", "stock"), bad}, "next_cursor": ""})
	}))
	defer server.Close()
	client, err := newHTTP(config.DatasourceConfig{BaseURL: server.URL, IncludeETF: false})
	if err != nil {
		t.Fatal(err)
	}
	if items, err := client.ListInstruments(context.Background()); !errors.Is(err, datasource.ErrProviderResponse) || items != nil {
		t.Fatal("disabled ETF filtering concealed malformed data")
	}
}
