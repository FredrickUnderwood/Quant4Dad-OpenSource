package service

import (
	"context"
	"errors"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/repository/coldstore"
	"github.com/quant4dad/internal/repository/datasource"
)

var ErrIntegrationInput = errors.New("invalid integration settings")
var ErrIntegrationStore = repository.ErrIntegrationStore

type CredentialChange struct {
	Action string `json:"action"`
	Value  string `json:"value,omitempty"`
}
type AutoSyncInput struct {
	Enabled   bool   `json:"enabled"`
	DailyTime string `json:"daily_time"`
}
type MarketIntegrationInput struct {
	Provider        string `json:"provider"`
	InitialYears    int    `json:"initial_years"`
	Concurrency     int    `json:"concurrency"`
	RateLimitPerMin int    `json:"rate_limit_per_min"`
	IncludeETF      bool   `json:"include_etf"`
	Tushare         struct {
		Token CredentialChange `json:"token"`
	} `json:"tushare"`
	HTTP struct {
		BaseURL string           `json:"base_url"`
		Token   CredentialChange `json:"token"`
	} `json:"http"`
	AutoSync AutoSyncInput `json:"auto_sync"`
}
type ArchiveIntegrationInput struct {
	Enabled       bool   `json:"enabled"`
	DailyTime     string `json:"daily_time"`
	RetentionDays int    `json:"retention_days"`
	BatchSize     int    `json:"batch_size"`
	BatchSleepMS  int    `json:"batch_sleep_ms"`
	OSS           struct {
		Endpoint        string           `json:"endpoint"`
		Region          string           `json:"region"`
		Bucket          string           `json:"bucket"`
		Prefix          string           `json:"prefix"`
		AccessKeyID     CredentialChange `json:"access_key_id"`
		AccessKeySecret CredentialChange `json:"access_key_secret"`
	} `json:"oss"`
}
type IntegrationPatch struct {
	ExpectedRevision *uint64                  `json:"expected_revision"`
	Market           *MarketIntegrationInput  `json:"market,omitempty"`
	Archive          *ArchiveIntegrationInput `json:"archive,omitempty"`
}
type MarketIntegrationView struct {
	Provider        string `json:"provider"`
	InitialYears    int    `json:"initial_years"`
	Concurrency     int    `json:"concurrency"`
	RateLimitPerMin int    `json:"rate_limit_per_min"`
	IncludeETF      bool   `json:"include_etf"`
	Tushare         struct {
		HasToken bool `json:"has_token"`
	} `json:"tushare"`
	HTTP struct {
		BaseURL  string `json:"base_url"`
		HasToken bool   `json:"has_token"`
	} `json:"http"`
	AutoSync AutoSyncInput `json:"auto_sync"`
}
type ArchiveIntegrationView struct {
	Enabled       bool   `json:"enabled"`
	DailyTime     string `json:"daily_time"`
	RetentionDays int    `json:"retention_days"`
	BatchSize     int    `json:"batch_size"`
	BatchSleepMS  int    `json:"batch_sleep_ms"`
	OSS           struct {
		Endpoint           string `json:"endpoint"`
		Region             string `json:"region"`
		Bucket             string `json:"bucket"`
		Prefix             string `json:"prefix"`
		HasAccessKeyID     bool   `json:"has_access_key_id"`
		HasAccessKeySecret bool   `json:"has_access_key_secret"`
	} `json:"oss"`
}
type IntegrationSettingsView struct {
	Revision            uint64                 `json:"revision"`
	Market              MarketIntegrationView  `json:"market"`
	Archive             ArchiveIntegrationView `json:"archive"`
	BackgroundEnabled   bool                   `json:"background_enabled"`
	RegisteredProviders []string               `json:"registered_providers"`
}

type IntegrationSettingService struct {
	repo *repository.IntegrationSettingsRepository
}

type IntegrationClients struct {
	Datasource datasource.Client
	Coldstore  coldstore.ColdStore
	OSSProbe   interface{ Probe(context.Context) error }
}

// Prepare constructs clients without contacting upstreams. A bad configuration
// never replaces either the durable snapshot or the active clients.
func (s *IntegrationSettingService) Prepare(c config.IntegrationDocument) (IntegrationClients, error) {
	var out IntegrationClients
	if err := ValidateIntegrations(c); err != nil {
		return out, err
	}
	client, err := datasource.New(c.Market.Datasource())
	if err != nil {
		return out, errors.New("data source configuration rejected")
	}
	out.Datasource = client
	if ArchiveConfigured(c.Archive) {
		cfg := c.Archive.OSS
		cfg.Endpoint = normalizeOSSEndpoint(cfg.Endpoint)
		store, err := coldstore.NewOSSStore(cfg)
		if err != nil {
			return out, errors.New("OSS configuration rejected")
		}
		out.Coldstore = store
		if probe, ok := any(store).(interface{ Probe(context.Context) error }); ok {
			out.OSSProbe = probe
		}
	}
	return out, nil
}

func NewIntegrationSettingService(repo *repository.IntegrationSettingsRepository) (*IntegrationSettingService, error) {
	if repo == nil {
		return nil, repository.ErrIntegrationStore
	}
	if err := ValidateIntegrations(repo.Snapshot()); err != nil {
		return nil, err
	}
	return &IntegrationSettingService{repo: repo}, nil
}
func (s *IntegrationSettingService) Snapshot() config.IntegrationDocument { return s.repo.Snapshot() }
func (s *IntegrationSettingService) Save(ctx context.Context, expected uint64, next config.IntegrationDocument) (config.IntegrationDocument, error) {
	if err := ValidateIntegrations(next); err != nil {
		return config.IntegrationDocument{}, err
	}
	return s.repo.Replace(ctx, expected, next)
}
func (s *IntegrationSettingService) Candidate(p IntegrationPatch) (config.IntegrationDocument, error) {
	c := s.Snapshot()
	if p.ExpectedRevision == nil || (p.Market == nil && p.Archive == nil) {
		return c, ErrIntegrationInput
	}
	if *p.ExpectedRevision != c.Revision {
		return c, domain.ErrResourceConflict
	}
	if in := p.Market; in != nil {
		m := &c.Market
		m.Provider, m.InitialYears, m.Concurrency, m.RateLimitPerMin, m.IncludeETF = in.Provider, in.InitialYears, in.Concurrency, in.RateLimitPerMin, in.IncludeETF
		var err error
		m.Tushare.Token, err = changeCredential(m.Tushare.Token, in.Tushare.Token)
		if err != nil {
			return c, err
		}
		// Moving a credential to another server is always an explicit operation.
		if in.HTTP.BaseURL != m.HTTP.BaseURL && m.HTTP.Token != "" && (in.HTTP.Token.Action == "" || in.HTTP.Token.Action == "keep") {
			return c, errors.New("changing the HTTP endpoint requires replacing or clearing its token")
		}
		m.HTTP.Token, err = changeCredential(m.HTTP.Token, in.HTTP.Token)
		if err != nil {
			return c, err
		}
		m.HTTP.BaseURL = in.HTTP.BaseURL
		m.AutoSync = config.AutoSyncConfig{Enabled: in.AutoSync.Enabled, DailyTime: in.AutoSync.DailyTime}
	}
	if in := p.Archive; in != nil {
		a := &c.Archive
		a.Enabled, a.DailyTime, a.RetentionDays, a.BatchSize, a.BatchSleepMS = in.Enabled, in.DailyTime, in.RetentionDays, in.BatchSize, in.BatchSleepMS
		o := &a.OSS
		endpoint := normalizeOSSEndpoint(in.OSS.Endpoint)
		if endpoint != normalizeOSSEndpoint(o.Endpoint) && (o.AccessKeyID != "" || o.AccessKeySecret != "") && ((in.OSS.AccessKeyID.Action == "" || in.OSS.AccessKeyID.Action == "keep") || (in.OSS.AccessKeySecret.Action == "" || in.OSS.AccessKeySecret.Action == "keep")) {
			return c, errors.New("changing the OSS endpoint requires replacing or clearing both credentials")
		}
		var err error
		o.AccessKeyID, err = changeCredential(o.AccessKeyID, in.OSS.AccessKeyID)
		if err != nil {
			return c, err
		}
		o.AccessKeySecret, err = changeCredential(o.AccessKeySecret, in.OSS.AccessKeySecret)
		if err != nil {
			return c, err
		}
		o.Endpoint, o.Region, o.Bucket, o.Prefix = endpoint, in.OSS.Region, in.OSS.Bucket, in.OSS.Prefix
	}
	return c, ValidateIntegrations(c)
}

func changeCredential(old string, change CredentialChange) (string, error) {
	switch change.Action {
	case "", "keep":
		if change.Value != "" {
			return "", ErrIntegrationInput
		}
		return old, nil
	case "clear":
		if change.Value != "" {
			return "", ErrIntegrationInput
		}
		return "", nil
	case "replace":
		if !validCredential(change.Value) || change.Value == "" {
			return "", ErrIntegrationInput
		}
		return change.Value, nil
	default:
		return "", ErrIntegrationInput
	}
}
func validCredential(s string) bool {
	return len(s) <= 4096 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\r\n\x00\t")
}
func validClock(s string) bool {
	t, e := time.Parse("15:04", s)
	return e == nil && t.Format("15:04") == s
}

var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
var regionPattern = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

func normalizeOSSEndpoint(s string) string {
	if s != "" && !strings.Contains(s, "://") {
		return "https://" + s
	}
	return s
}
func validEndpoint(s string, httpsOnly bool) bool {
	if len(s) > 2048 || strings.TrimSpace(s) != s || strings.ContainsAny(s, "\r\n\x00\t ") {
		return false
	}
	u, e := url.Parse(s)
	return e == nil && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.Opaque == "" && (u.Scheme == "https" || (!httpsOnly && u.Scheme == "http"))
}
func ArchiveConfigured(a config.ArchiveConfig) bool {
	o := a.OSS
	return o.Endpoint != "" && o.Region != "" && o.Bucket != "" && o.AccessKeyID != "" && o.AccessKeySecret != ""
}
func ValidateIntegrations(c config.IntegrationDocument) error {
	m := c.Market
	if c.SchemaVersion != 1 || m.InitialYears < 1 || m.InitialYears > 50 || m.Concurrency < 1 || m.Concurrency > 32 || m.RateLimitPerMin < 0 || m.RateLimitPerMin > 60000 || !validClock(m.AutoSync.DailyTime) {
		return ErrIntegrationInput
	}
	if !validCredential(m.Tushare.Token) || !validCredential(m.HTTP.Token) {
		return ErrIntegrationInput
	}
	if m.HTTP.BaseURL != "" && !validEndpoint(m.HTTP.BaseURL, false) {
		return errors.New("HTTP data source requires an http(s) base URL without credentials or query parameters")
	}
	switch m.Provider {
	case "manual":
		if m.AutoSync.Enabled {
			return errors.New("automatic collection requires a configured data source")
		}
	case "tushare":
		if m.Tushare.Token == "" {
			return errors.New("Tushare token is required")
		}
	case "http":
		if m.HTTP.BaseURL == "" {
			return errors.New("HTTP data source URL is required")
		}
	default:
		registered := false
		for _, name := range datasource.Providers() {
			if name == m.Provider {
				registered = true
				break
			}
		}
		if !registered {
			return errors.New("unsupported data source")
		}
	}
	a := c.Archive
	o := a.OSS
	if !validClock(a.DailyTime) || a.RetentionDays < 1 || a.RetentionDays > 36500 || a.BatchSize < 1 || a.BatchSize > 2000 || a.BatchSleepMS < 0 || a.BatchSleepMS > 60000 {
		return ErrIntegrationInput
	}
	if o.Endpoint != "" {
		endpoint := normalizeOSSEndpoint(o.Endpoint)
		if !validEndpoint(endpoint, true) {
			return errors.New("OSS requires an HTTPS endpoint without credentials or query parameters")
		}
		u, _ := url.Parse(endpoint)
		if u.Path != "" && u.Path != "/" {
			return errors.New("OSS endpoint must not contain a path")
		}
	}
	if o.Bucket != "" && !bucketPattern.MatchString(o.Bucket) {
		return errors.New("invalid OSS bucket name")
	}
	if o.Region != "" && !regionPattern.MatchString(o.Region) {
		return errors.New("invalid OSS region")
	}
	if len(o.Prefix) > 256 || strings.HasPrefix(o.Prefix, "/") || strings.Contains(o.Prefix, "..") || strings.ContainsAny(o.Prefix, "\r\n\x00\\") || !validCredential(o.AccessKeyID) || !validCredential(o.AccessKeySecret) {
		return ErrIntegrationInput
	}
	if prefix := strings.TrimSuffix(o.Prefix, "/"); o.Prefix != "" && (prefix == "" || prefix == "." || path.Clean(prefix) != prefix) {
		return errors.New("OSS prefix must use canonical path segments")
	}
	if a.Enabled && !ArchiveConfigured(a) {
		return errors.New("complete OSS connection settings before enabling archive")
	}
	return nil
}
func IntegrationView(c config.IntegrationDocument, background bool) IntegrationSettingsView {
	v := IntegrationSettingsView{Revision: c.Revision, BackgroundEnabled: background, RegisteredProviders: datasource.Providers()}
	m := c.Market
	v.Market = MarketIntegrationView{Provider: m.Provider, InitialYears: m.InitialYears, Concurrency: m.Concurrency, RateLimitPerMin: m.RateLimitPerMin, IncludeETF: m.IncludeETF, AutoSync: AutoSyncInput{Enabled: m.AutoSync.Enabled, DailyTime: m.AutoSync.DailyTime}}
	v.Market.Tushare.HasToken = m.Tushare.Token != ""
	v.Market.HTTP.BaseURL = m.HTTP.BaseURL
	v.Market.HTTP.HasToken = m.HTTP.Token != ""
	a := c.Archive
	o := a.OSS
	v.Archive = ArchiveIntegrationView{Enabled: a.Enabled, DailyTime: a.DailyTime, RetentionDays: a.RetentionDays, BatchSize: a.BatchSize, BatchSleepMS: a.BatchSleepMS}
	v.Archive.OSS.Endpoint = normalizeOSSEndpoint(o.Endpoint)
	v.Archive.OSS.Region = o.Region
	v.Archive.OSS.Bucket = o.Bucket
	v.Archive.OSS.Prefix = o.Prefix
	v.Archive.OSS.HasAccessKeyID = o.AccessKeyID != ""
	v.Archive.OSS.HasAccessKeySecret = o.AccessKeySecret != ""
	return v
}
