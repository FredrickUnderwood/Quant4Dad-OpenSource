package datasource

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
)

// Constructor builds a market-data Client from config. cfg is the datasource
// section of config.yaml, so provider-specific fields (token, base_url, …) are
// all available here.
type Constructor func(cfg config.DatasourceConfig) (Client, error)

var (
	providerMu sync.RWMutex
	providers  = make(map[string]Constructor)
)

// Register records a market-data implementation. name is the value that
// datasource.provider takes in config.yaml. The convention is to call this from
// the implementation package's init(), then blank-import that package from main:
//
//	func init() { datasource.Register("myprovider", newMyProvider) }
//
// Registering the same provider name twice panics — that is a build-time
// programming error, better surfaced immediately.
func Register(name string, ctor Constructor) {
	if name == "" {
		panic("datasource: Register with empty provider name")
	}
	if ctor == nil {
		panic("datasource: Register with nil constructor for provider " + name)
	}
	providerMu.Lock()
	defer providerMu.Unlock()
	if _, dup := providers[name]; dup {
		panic("datasource: duplicate provider registration: " + name)
	}
	providers[name] = ctor
}

// Providers returns the registered provider names in ascending order, for
// startup logs and error messages.
func Providers() []string {
	providerMu.RLock()
	defer providerMu.RUnlock()
	out := make([]string, 0, len(providers))
	for name := range providers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// New looks up cfg.Provider in the registry and constructs the Client.
//
// Empty and manual explicitly disable network providers. Registration never
// enables a source implicitly; every other name must be registered.
// When RateLimitPerMin > 0, built-in providers apply the quota per HTTP request.
func New(cfg config.DatasourceConfig) (Client, error) {
	name := cfg.Provider
	if name == "" || name == "manual" {
		return Unavailable(), nil
	}
	names := Providers()

	providerMu.RLock()
	ctor, ok := providers[name]
	providerMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown datasource provider %q, registered providers: %v", name, names)
	}

	client, err := ctor(cfg)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, fmt.Errorf("datasource provider %q returned a nil client", name)
	}
	if lim := newRateLimiter(cfg.RateLimitPerMin); lim != nil {
		if perRequest, ok := client.(RequestLimitedClient); ok {
			perRequest.SetRequestLimiter(lim.Wait)
		} else {
			client = &rateLimitedClient{Client: client, lim: lim}
		}
	}
	return client, nil
}

// ErrNoProvider is returned when network fetching is explicitly disabled.
// Data-sync returns it before creating a task.
var ErrNoProvider = errors.New("在设置中选择并配置数据源或导入 CSV")

// Unavailable returns a Client placeholder that always fails, so "no data source
// plugged in" shows up as "the sync endpoint returns a clear error" rather than
// "the process won't boot".
func Unavailable() Client { return unavailableClient{} }

type unavailableClient struct{}

func (unavailableClient) Name() string { return "unavailable" }

func (unavailableClient) Probe(context.Context) error { return ErrNoProvider }

func (unavailableClient) ListInstruments(context.Context) ([]*domain.Instrument, error) {
	return nil, ErrNoProvider
}

func (unavailableClient) FetchBars(context.Context, string, domain.BarPeriod, time.Time, time.Time) ([]*domain.Bar, error) {
	return nil, ErrNoProvider
}

// IsUnavailable detects the built-in placeholder without relying on a provider
// name or making a network call. Custom provider failures keep their normal path.
func IsUnavailable(client Client) bool {
	switch c := client.(type) {
	case nil, unavailableClient:
		return true
	case *rateLimitedClient:
		return c == nil || IsUnavailable(c.Client)
	default:
		return false
	}
}
