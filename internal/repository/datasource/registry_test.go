package datasource

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
)

// registerForTest registers a test provider and unregisters it when the case
// ends, so it cannot leak into other cases.
func registerForTest(t *testing.T, name string, ctor Constructor) {
	t.Helper()
	Register(name, ctor)
	t.Cleanup(func() {
		providerMu.Lock()
		delete(providers, name)
		providerMu.Unlock()
	})
}

func stubCtor(name string) Constructor {
	return func(config.DatasourceConfig) (Client, error) { return stubClient{name: name}, nil }
}

// With nothing registered, New must not error and must return the placeholder, so
// the service still boots (backtests and pipelines don't need market-data fetching).
func TestNewWithoutProvidersReturnsUnavailable(t *testing.T) {
	client, err := New(config.DatasourceConfig{})
	if err != nil {
		t.Fatalf("should not error when no implementation is registered: %v", err)
	}
	if _, err := client.ListInstruments(context.Background()); !errors.Is(err, ErrNoProvider) {
		t.Errorf("want ErrNoProvider, got %v", err)
	}
	if _, err := client.FetchBars(context.Background(), "000001", domain.Bar1d, time.Now(), time.Now()); !errors.Is(err, ErrNoProvider) {
		t.Errorf("want ErrNoProvider, got %v", err)
	}
}

// With exactly one implementation registered and no provider named in config, use it.
func TestNewDefaultsToSoleProvider(t *testing.T) {
	registerForTest(t, "only", stubCtor("only"))
	client, err := New(config.DatasourceConfig{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if client.Name() != "only" {
		t.Errorf("want only, got %s", client.Name())
	}
}

// With several implementations registered, the provider must be named explicitly
// rather than one being picked arbitrarily.
func TestNewRequiresProviderWhenAmbiguous(t *testing.T) {
	registerForTest(t, "one", stubCtor("one"))
	registerForTest(t, "two", stubCtor("two"))
	if _, err := New(config.DatasourceConfig{}); err == nil {
		t.Fatal("should error with several implementations and no provider named")
	}
	client, err := New(config.DatasourceConfig{Provider: "two"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if client.Name() != "two" {
		t.Errorf("want two, got %s", client.Name())
	}
}

// A misspelled provider name errors instead of silently falling back, so
// production never quietly runs against an unintended source.
func TestNewUnknownProvider(t *testing.T) {
	registerForTest(t, "known", stubCtor("known"))
	if _, err := New(config.DatasourceConfig{Provider: "typo"}); err == nil {
		t.Fatal("an unregistered provider name should error")
	}
}

// rate_limit_per_min > 0 wraps the client in the limiter decorator, which still
// passes Name through to the underlying implementation.
func TestNewWrapsRateLimiter(t *testing.T) {
	registerForTest(t, "limited", stubCtor("limited"))
	client, err := New(config.DatasourceConfig{Provider: "limited", RateLimitPerMin: 600})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if _, ok := client.(*rateLimitedClient); !ok {
		t.Fatalf("want *rateLimitedClient, got %T", client)
	}
	if client.Name() != "limited" {
		t.Errorf("the limiter decorator should pass Name through, got %s", client.Name())
	}
}

type stubClient struct{ name string }

func (c stubClient) Name() string { return c.name }

func (stubClient) ListInstruments(context.Context) ([]*domain.Instrument, error) { return nil, nil }

func (stubClient) FetchBars(context.Context, string, domain.BarPeriod, time.Time, time.Time) ([]*domain.Bar, error) {
	return nil, nil
}
