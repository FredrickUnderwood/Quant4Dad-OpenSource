package config

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Web        WebConfig        `yaml:"web"`
	MCP        MCPConfig        `yaml:"mcp"`
	Server     ServerConfig     `yaml:"server"`
	Log        LogConfig        `yaml:"log"`
	Security   SecurityConfig   `yaml:"security"`
	Storage    StorageConfig    `yaml:"storage"`
	Datasource DatasourceConfig `yaml:"datasource"`
	Backtest   BacktestConfig   `yaml:"backtest"`
	AutoSync   AutoSyncConfig   `yaml:"auto_sync"`
	News       NewsConfig       `yaml:"news"`
	Archive    ArchiveConfig    `yaml:"archive"`
	Agent      AgentConfig      `yaml:"agent"`
}

// AgentConfig keeps the staged Agent API surface opt-in. Enabling it currently
// exposes model management and optional bootstrap/probe/Session metadata; Run
// admission is still a separate application feature.
type AgentConfig struct {
	ProfileSource string                `yaml:"profile_source"` // repository (default) or explicit config pin.
	Enabled       bool                  `yaml:"enabled"`
	Bootstrap     AgentBootstrapConfig  `yaml:"bootstrap"`
	ModelProbe    AgentModelProbeConfig `yaml:"model_probe"`
	Sessions      AgentSessionsConfig   `yaml:"sessions"`
	Runs          AgentRunsConfig       `yaml:"runs"`
	Gateway       AgentGatewayConfig    `yaml:"gateway"`
}

type ServerConfig struct {
	Addr string `yaml:"addr"`
	// TrustedProxies empty = trust no proxy; keep that default when exposing the
	// service directly to the internet. Behind an nginx/traefik reverse proxy, put
	// the proxy's private ranges here (e.g. ["127.0.0.1", "172.16.0.0/12"]).
	TrustedProxies []string `yaml:"trusted_proxies"`
}

// SecurityConfig provides the lightest possible token login: entering a matching
// token on the UI's login page sets an HttpOnly cookie, and subsequent /api/v1/*
// requests are authorized by that cookie. An empty token means no login at all,
// which is fine for local development. Always set it when exposing the service
// publicly; store credentials in a private local YAML file.
type SecurityConfig struct {
	Token string `yaml:"token"`
}

type LogConfig struct {
	Level    string `yaml:"level"`
	BaseName string `yaml:"base_name"`
}

// StorageConfig selects the underlying storage backend.
// Metadata (Strategy / Cost / BacktestJob / Trade / Equity / DataSync / Instrument)
// always lives in a SQL store. When backend=csv, metadata falls back to the embedded
// sqlite store and only bar data is read/written from CSV files.
type StorageConfig struct {
	Backend string       `yaml:"backend"` // csv / sqlite / mysql
	SQLite  SQLiteConfig `yaml:"sqlite"`
	MySQL   MySQLConfig  `yaml:"mysql"`
	CSV     CSVConfig    `yaml:"csv"`
}

type SQLiteConfig struct {
	Path string `yaml:"path"`
}

type MySQLConfig struct {
	DSN             string        `yaml:"dsn"`
	MaxOpenConns    int           `yaml:"max_open_conns"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
}

type CSVConfig struct {
	BaseDir string `yaml:"base_dir"`
}

// DatasourceConfig configures a user-installed data provider. No network data
// collectors are included; local CSV import works without a provider.
type DatasourceConfig struct {
	Provider        string `yaml:"provider"`           // a name registered via datasource.Register; may be empty when only one is registered
	Token           string `yaml:"token"`              // credential, if the implementation needs one
	BaseURL         string `yaml:"base_url"`           // optional endpoint override
	InitialYears    int    `yaml:"initial_years"`      // how many years the first full backfill reaches back
	Concurrency     int    `yaml:"concurrency"`        // number of sync workers
	RateLimitPerMin int    `yaml:"rate_limit_per_min"` // per-minute cap on upstream calls; <=0 disables. The framework applies this for you.
	IncludeETF      bool   `yaml:"include_etf"`        // Include ETF assets when supported by an installed provider
}

type BacktestConfig struct {
	WorkerPool int `yaml:"worker_pool"`
}

// AutoSyncConfig controls an optional schedule for a user-installed provider.
// It is disabled by default.
type AutoSyncConfig struct {
	Enabled   bool   `yaml:"enabled"`
	DailyTime string `yaml:"daily_time"` // "HH:MM" 24h local
}

// NewsConfig controls the news collection scheduler. With Enabled=false no
// collector goroutine is started. BaseIntervalSeconds is the base poll tick: the
// scheduler wakes up that often and fetches from every source that is enabled and
// whose own interval has elapsed. Each source's enabled flag and interval live in
// the setting table (editable from the UI), so only the base tick is needed here.
type NewsConfig struct {
	Enabled             bool `yaml:"enabled"`
	BaseIntervalSeconds int  `yaml:"base_interval_seconds"`
}

// ArchiveConfig controls hot/cold tiering for the three event tables
// (pipeline_event / _trace / _ai_result): once a day at DailyTime an archive job
// exports whole days older than RetentionDays (by received_at) to object storage
// as jsonl.gz — written once, almost never read back — then, having verified the
// upload, deletes them from MySQL in throttled batches. The news layer is not
// archived yet because it carries the deduplication role (handled in phase 2).
type ArchiveConfig struct {
	Enabled       bool      `yaml:"enabled"`
	DailyTime     string    `yaml:"daily_time"`     // "HH:MM" 24h local; stagger it away from auto_sync (03:00)
	RetentionDays int       `yaml:"retention_days"` // how many days of hot data MySQL keeps; older days are archived
	BatchSize     int       `yaml:"batch_size"`     // rows per export/delete batch
	BatchSleepMS  int       `yaml:"batch_sleep_ms"` // pause between batches, so deletion doesn't crush a live DB
	OSS           OSSConfig `yaml:"oss"`
}

// OSSConfig holds Alibaba Cloud OSS connection parameters from the private YAML.
type OSSConfig struct {
	Endpoint        string `yaml:"endpoint"`          // e.g. oss-cn-hangzhou.aliyuncs.com (use the -internal domain from inside ECS)
	Region          string `yaml:"region"`            // e.g. cn-hangzhou
	Bucket          string `yaml:"bucket"`            //
	Prefix          string `yaml:"prefix"`            // object key prefix, e.g. quant4dad/
	AccessKeyID     string `yaml:"access_key_id"`     //
	AccessKeySecret string `yaml:"access_key_secret"` //
}

const (
	StorageBackendCSV    = "csv"
	StorageBackendSQLite = "sqlite"
	StorageBackendMySQL  = "mysql"
)

func defaults() *Config {
	return &Config{
		Server: ServerConfig{Addr: ":8080"},
		MCP:    MCPConfig{Addr: ":8080"},
		Web:    WebConfig{Addr: ":8080", APIBaseURL: "http://127.0.0.1:8080"},
		Log:    LogConfig{Level: "info"},
		Storage: StorageConfig{
			Backend: StorageBackendSQLite,
			SQLite:  SQLiteConfig{Path: "./data/quant4dad.db"},
			MySQL: MySQLConfig{
				MaxOpenConns:    20,
				MaxIdleConns:    10,
				ConnMaxLifetime: time.Hour,
			},
			CSV: CSVConfig{BaseDir: "./data/bars"},
		},
		Datasource: DatasourceConfig{
			Provider:        "",
			InitialYears:    10,
			Concurrency:     8,
			RateLimitPerMin: 300,
		},
		Backtest: BacktestConfig{WorkerPool: 4},
		AutoSync: AutoSyncConfig{Enabled: false, DailyTime: "03:00"},
		News:     NewsConfig{Enabled: false, BaseIntervalSeconds: 30},
		Archive: ArchiveConfig{
			Enabled:       false, // off by default; turn it on once OSS is configured
			DailyTime:     "03:30",
			RetentionDays: 10,
			BatchSize:     2000,
			BatchSleepMS:  200,
			OSS:           OSSConfig{Prefix: "quant4dad/"},
		},
	}
}

// Load reads one complete local YAML. Business environment overrides are not
// supported. Every process uses the same private local configuration file.
func Load(flagPath string) (*Config, error) {
	path := resolveConfigPath(flagPath)
	if path == "" {
		return nil, errors.New("configuration file is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse rejects unknown fields, empty/multiple documents and invalid settings.
// It never includes raw YAML or credential values in errors.
func Parse(data []byte) (*Config, error) {
	cfg := defaults()
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var document yaml.Node
	if yaml.Unmarshal(data, &document) != nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("invalid configuration document")
	}
	if decoder.Decode(cfg) != nil {
		return nil, errors.New("invalid configuration fields")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("only one configuration document is allowed")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// All consumers parse and validate the same complete snapshot.
func LoadForApp(path string) (*Config, error) { return Load(path) }
func LoadForMCP(path string) (*Config, error) { return Load(path) }

func (c *Config) Validate() error {
	if err := c.validate(); err != nil {
		return err
	}
	for _, addr := range []string{c.Server.Addr, c.MCP.Addr, c.Web.Addr} {
		_, port, err := net.SplitHostPort(addr)
		n, numberErr := strconv.Atoi(port)
		if err != nil || numberErr != nil || n < 1 || n > 65535 {
			return errors.New("invalid service listen address")
		}
	}
	if err := c.Web.Validate(); err != nil {
		return err
	}
	for _, proxy := range c.Server.TrustedProxies {
		if net.ParseIP(proxy) == nil {
			if _, _, err := net.ParseCIDR(proxy); err != nil {
				return errors.New("invalid server.trusted_proxies")
			}
		}
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("invalid log.level")
	}
	if c.Backtest.WorkerPool < 1 || c.Datasource.Concurrency < 1 || c.Datasource.InitialYears < 1 {
		return errors.New("invalid worker or datasource configuration")
	}
	if _, err := time.Parse("15:04", c.AutoSync.DailyTime); err != nil {
		return errors.New("invalid auto_sync.daily_time")
	}
	if c.News.Enabled && c.News.BaseIntervalSeconds < 1 {
		return errors.New("invalid news.base_interval_seconds")
	}
	if c.Archive.Enabled || c.Archive.OSS.Bucket != "" {
		if err := c.Archive.ValidateOSS(); err != nil {
			return err
		}
		if _, err := time.Parse("15:04", c.Archive.DailyTime); err != nil {
			return errors.New("invalid archive.daily_time")
		}
		if c.Archive.BatchSize < 1 || c.Archive.BatchSleepMS < 0 {
			return errors.New("invalid archive batch settings")
		}
	}
	for _, validate := range []func() error{c.loadAgentProfiles, c.loadAgentBootstrap, c.loadAgentModelProbe, c.loadAgentRuns} {
		if err := validate(); err != nil {
			return err
		}
	}
	if c.Agent.Sessions.Enabled {
		if err := c.ValidateAgentSessions(); err != nil {
			return err
		}
	}
	if c.Agent.Gateway.Enabled {
		if err := c.ValidateAgentGateway(); err != nil {
			return err
		}
	}
	if c.MCP.ExternalToken != "" || c.MCP.AgentToolsEnabled || c.MCP.APIBaseURL != "" {
		return c.ValidateExternalMCP()
	}
	return nil
}

type WebConfig struct {
	Addr           string   `yaml:"addr"`
	APIBaseURL     string   `yaml:"api_base_url"`
	TrustedProxies []string `yaml:"trusted_proxies"`
}

func (c WebConfig) Validate() error {
	for _, proxy := range c.TrustedProxies {
		if net.ParseIP(proxy) == nil {
			if _, _, err := net.ParseCIDR(proxy); err != nil {
				return errors.New("invalid web.trusted_proxies")
			}
		}
	}
	u, err := url.Parse(c.APIBaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(c.APIBaseURL, "\r\n\t #") {
		return errors.New("invalid web.api_base_url")
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return errors.New("invalid web.api_base_url port")
		}
	}
	return nil
}

func (Config) String() string   { return "Config{redacted}" }
func (Config) GoString() string { return "Config{redacted}" }

func (c *Config) validate() error {
	switch c.Storage.Backend {
	case StorageBackendCSV, StorageBackendSQLite, StorageBackendMySQL:
	default:
		return errors.New("invalid storage.backend, expect csv / sqlite / mysql")
	}
	if c.Storage.Backend == StorageBackendMySQL && c.Storage.MySQL.DSN == "" {
		return errors.New("storage.mysql.dsn is required when backend=mysql")
	}
	if c.Storage.Backend == StorageBackendCSV && c.Storage.CSV.BaseDir == "" {
		return errors.New("storage.csv.base_dir is required when backend=csv")
	}
	if c.Storage.Backend != StorageBackendMySQL && c.Storage.SQLite.Path == "" {
		return errors.New("storage.sqlite.path is required")
	}
	if c.Storage.MySQL.MaxOpenConns < 1 || c.Storage.MySQL.MaxIdleConns < 0 || c.Storage.MySQL.MaxIdleConns > c.Storage.MySQL.MaxOpenConns || c.Storage.MySQL.ConnMaxLifetime < 0 {
		return errors.New("invalid MySQL connection pool settings")
	}
	return nil
}

// ValidateOSS checks that the OSS config needed for archiving is complete; called
// by LoadForApp when archive.enabled is set.
func (a ArchiveConfig) ValidateOSS() error {
	o := a.OSS
	if o.Endpoint == "" || o.Bucket == "" || o.AccessKeyID == "" || o.AccessKeySecret == "" {
		return errors.New("archive.oss endpoint/bucket/access_key_id/access_key_secret are required when archive.enabled=true")
	}
	if a.RetentionDays <= 0 {
		return errors.New("archive.retention_days must be > 0 when archive.enabled=true")
	}
	return nil
}

func resolveConfigPath(flagPath string) string {
	if flagPath != "" {
		return flagPath
	}
	if v := os.Getenv("QUANT4DAD_CONFIG"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		"./config/quant4dad.yaml",
		filepath.Join(home, ".config/quant4dad/quant4dad.yaml"),
		"/etc/quant4dad/quant4dad.yaml",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
