package config

// IntegrationDocument is stored in a private local file, never in the public
// deployment YAML or generic SQL settings table. All members are value types.
type IntegrationDocument struct {
	SchemaVersion int                     `yaml:"schema_version"`
	Revision      uint64                  `yaml:"revision"`
	Market        MarketIntegrationConfig `yaml:"market"`
	Archive       ArchiveConfig           `yaml:"archive"`
}

type MarketIntegrationConfig struct {
	Provider        string             `yaml:"provider"`
	InitialYears    int                `yaml:"initial_years"`
	Concurrency     int                `yaml:"concurrency"`
	RateLimitPerMin int                `yaml:"rate_limit_per_min"`
	IncludeETF      bool               `yaml:"include_etf"`
	Tushare         TushareIntegration `yaml:"tushare"`
	HTTP            HTTPIntegration    `yaml:"http"`
	AutoSync        AutoSyncConfig     `yaml:"auto_sync"`
}

type TushareIntegration struct {
	Token string `yaml:"token"`
}

type HTTPIntegration struct {
	BaseURL string `yaml:"base_url"`
	Token   string `yaml:"token"`
}

func (m MarketIntegrationConfig) Datasource() DatasourceConfig {
	c := DatasourceConfig{Provider: m.Provider, InitialYears: m.InitialYears,
		Concurrency: m.Concurrency, RateLimitPerMin: m.RateLimitPerMin, IncludeETF: m.IncludeETF}
	switch m.Provider {
	case "tushare":
		c.Token = m.Tushare.Token
	default:
		c.Token, c.BaseURL = m.HTTP.Token, m.HTTP.BaseURL
	}
	return c
}

func InitialIntegrations(c *Config) IntegrationDocument {
	p := c.Datasource.Provider
	if p == "" {
		p = "manual"
	}
	m := MarketIntegrationConfig{Provider: p, InitialYears: c.Datasource.InitialYears,
		Concurrency: c.Datasource.Concurrency, RateLimitPerMin: c.Datasource.RateLimitPerMin,
		IncludeETF: c.Datasource.IncludeETF, AutoSync: c.AutoSync}
	if p == "tushare" {
		m.Tushare.Token = c.Datasource.Token
	}
	if p != "manual" && p != "tushare" {
		m.HTTP = HTTPIntegration{BaseURL: c.Datasource.BaseURL, Token: c.Datasource.Token}
	}
	return IntegrationDocument{SchemaVersion: 1, Market: m, Archive: c.Archive}
}

func (IntegrationDocument) String() string   { return "IntegrationDocument{redacted}" }
func (IntegrationDocument) GoString() string { return "IntegrationDocument{redacted}" }
