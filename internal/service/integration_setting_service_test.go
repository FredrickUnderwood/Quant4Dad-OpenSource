package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
)

func integrationServiceFixture(t *testing.T) *IntegrationSettingService {
	t.Helper()
	cfg, err := config.Parse([]byte("server:\n  addr: ':8080'\n"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := repository.NewIntegrationSettingsRepository(filepath.Join(t.TempDir(), "private", "integrations.yaml"), config.InitialIntegrations(cfg))
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewIntegrationSettingService(r)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func marketIntegrationInput(d config.IntegrationDocument) *MarketIntegrationInput {
	m := d.Market
	in := &MarketIntegrationInput{Provider: m.Provider, InitialYears: m.InitialYears, Concurrency: m.Concurrency, RateLimitPerMin: m.RateLimitPerMin, IncludeETF: m.IncludeETF, AutoSync: AutoSyncInput{Enabled: m.AutoSync.Enabled, DailyTime: m.AutoSync.DailyTime}}
	in.HTTP.BaseURL = m.HTTP.BaseURL
	return in
}
func TestIntegrationCredentialPreservationMaskingAndDestinationChange(t *testing.T) {
	s := integrationServiceFixture(t)
	d := s.Snapshot()
	revision := d.Revision
	in := marketIntegrationInput(d)
	in.Provider = "tushare"
	in.Tushare.Token = CredentialChange{Action: "replace", Value: "synthetic-tushare-credential"}
	in.HTTP.BaseURL = "https://data.example.test"
	in.HTTP.Token = CredentialChange{Action: "replace", Value: "synthetic-http-credential"}
	next, err := s.Candidate(IntegrationPatch{ExpectedRevision: &revision, Market: in})
	if err != nil {
		t.Fatal(err)
	}
	d, err = s.Save(context.Background(), revision, next)
	if err != nil {
		t.Fatal(err)
	}
	body, err := sonic.Marshal(IntegrationView(d, true))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "synthetic-") || !strings.Contains(string(body), `"has_token":true`) {
		t.Fatal("credential view is not masked")
	}
	revision = d.Revision
	in = marketIntegrationInput(d)
	in.Provider = "http"
	next, err = s.Candidate(IntegrationPatch{ExpectedRevision: &revision, Market: in})
	if err != nil {
		t.Fatal(err)
	}
	if next.Market.HTTP.Token != d.Market.HTTP.Token || next.Market.Tushare.Token != d.Market.Tushare.Token {
		t.Fatal("provider switch lost separate credentials")
	}
	in.HTTP.BaseURL = "https://another.example.test"
	if _, err = s.Candidate(IntegrationPatch{ExpectedRevision: &revision, Market: in}); err == nil {
		t.Fatal("credential silently moved to another endpoint")
	}
	in.HTTP.Token = CredentialChange{Action: "clear"}
	next, err = s.Candidate(IntegrationPatch{ExpectedRevision: &revision, Market: in})
	if err != nil || next.Market.HTTP.Token != "" {
		t.Fatal("explicit clearing failed")
	}
	stale := uint64(0)
	if _, err = s.Candidate(IntegrationPatch{ExpectedRevision: &stale, Market: in}); !errors.Is(err, domain.ErrResourceConflict) {
		t.Fatal("stale revision accepted")
	}
}
func TestIntegrationValidationRejectsInvalidOrDestructiveDefaults(t *testing.T) {
	s := integrationServiceFixture(t)
	base := s.Snapshot()
	cases := []func(*config.IntegrationDocument){
		func(d *config.IntegrationDocument) { d.Market.AutoSync.Enabled = true },
		func(d *config.IntegrationDocument) { d.Market.Provider = "tushare" },
		func(d *config.IntegrationDocument) { d.Market.Provider = "http" },
		func(d *config.IntegrationDocument) { d.Market.Concurrency = 0 },
		func(d *config.IntegrationDocument) { d.Market.AutoSync.DailyTime = "3:00" },
		func(d *config.IntegrationDocument) { d.Market.HTTP.BaseURL = "https://user:password@example.test" },
		func(d *config.IntegrationDocument) { d.Market.HTTP.BaseURL = "https://example.test/?token=secret" },
		func(d *config.IntegrationDocument) { d.Archive.Enabled = true },
		func(d *config.IntegrationDocument) { d.Archive.RetentionDays = 0 },
		func(d *config.IntegrationDocument) { d.Archive.OSS.Endpoint = "http://example.test" },
		func(d *config.IntegrationDocument) { d.Archive.OSS.Prefix = "../outside/" },
	}
	for i, change := range cases {
		candidate := base
		change(&candidate)
		if err := ValidateIntegrations(candidate); err == nil {
			t.Errorf("invalid configuration %d accepted", i)
		}
	}
	partial := base
	partial.Archive.OSS.Endpoint = "oss-cn-hangzhou.aliyuncs.com"
	partial.Archive.OSS.Region = "cn-hangzhou"
	partial.Archive.OSS.Bucket = "synthetic-bucket"
	if err := ValidateIntegrations(partial); err != nil || ArchiveConfigured(partial.Archive) {
		t.Fatal("disabled partial OSS configuration should be editable")
	}
	for _, change := range []CredentialChange{{Action: "keep", Value: "hidden"}, {Action: "clear", Value: "hidden"}, {Action: "replace"}, {Action: "replace", Value: "line\nbreak"}, {Action: "unknown"}} {
		if _, err := changeCredential("old", change); err == nil {
			t.Fatal("ambiguous credential mutation accepted")
		}
	}
}

func TestIntegrationArchiveLimitsMatchPublicationContract(t *testing.T) {
	s := integrationServiceFixture(t)
	before := s.Snapshot()
	for _, tc := range []struct {
		name   string
		prefix string
		batch  int
	}{
		{"empty-segment", "a//", 2000},
		{"dot-segment", "a/./", 2000},
		{"batch-over-limit", "a/", 2001},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := &ArchiveIntegrationInput{DailyTime: "04:00", RetentionDays: 30, BatchSize: tc.batch}
			in.OSS.Prefix = tc.prefix
			if _, err := s.Candidate(IntegrationPatch{ExpectedRevision: &before.Revision, Archive: in}); err == nil {
				t.Fatal("accepted archive configuration that cannot be honored")
			}
			if s.Snapshot() != before {
				t.Fatal("invalid configuration changed durable state")
			}
		})
	}
	in := &ArchiveIntegrationInput{DailyTime: "04:00", RetentionDays: 30, BatchSize: 2000}
	in.OSS.Prefix = "a/b/"
	if _, err := s.Candidate(IntegrationPatch{ExpectedRevision: &before.Revision, Archive: in}); err != nil {
		t.Fatal("valid publication boundary rejected", err)
	}
}
