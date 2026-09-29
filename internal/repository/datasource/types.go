// Package datasource defines interfaces and shared scaffolding for external data.
// Market-data includes explicitly selected official/custom HTTP adapters. News
// implementations remain user-installed extensions. See README.md for contracts.
package datasource

import (
	"context"
	"time"

	"github.com/quant4dad/internal/domain"
)

// Client is the abstraction for any external market-data provider.
// Implementations must return bars sorted ascending by date.
//
// Name reports the provider name, used in logs and data-sync records; it should
// match the name used at registration. ListInstruments returns every tradable
// instrument; FetchBars returns the bars within [start, end]. Wrap upstream
// failures with NewFetchError. Credential-bearing adapters must omit response
// bodies and use fixed errors so sync records cannot disclose credentials.
type Client interface {
	Name() string
	ListInstruments(ctx context.Context) ([]*domain.Instrument, error)
	FetchBars(ctx context.Context, code string, period domain.BarPeriod, start, end time.Time) ([]*domain.Bar, error)
}

// InstrumentBarsClient routes by persisted asset identity, without guessing an
// ETF from its name or code. Providers without it retain the Client contract.
type InstrumentBarsClient interface {
	FetchInstrumentBars(context.Context, *domain.Instrument, domain.BarPeriod, time.Time, time.Time) ([]*domain.Bar, error)
}

// RequestLimitedClient applies the shared quota to every HTTP request, including
// catalog subrequests and history pages inside one Client method.
type RequestLimitedClient interface {
	SetRequestLimiter(func(context.Context) error)
}

// Prober performs a bounded, read-only connectivity and credential check. It
// does not synchronize data or prove access to every upstream dataset.
type Prober interface {
	Probe(context.Context) error
}
