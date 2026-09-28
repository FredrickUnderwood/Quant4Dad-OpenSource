package service

import (
	"context"
	"errors"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository"
)

type SettingService struct {
	repo *repository.SettingRepository
}

func NewSettingService(repo *repository.SettingRepository) *SettingService {
	return &SettingService{repo: repo}
}

// ===================== Delivery channel: email (SMTP) =====================

// EmailSetting holds the connection parameters for the delivery nodes' email (SMTP)
// channel (the content of the setting).
type EmailSetting struct {
	Host      string   `json:"host"`       // SMTP server address
	Port      int      `json:"port"`       // SMTP port
	Username  string   `json:"username"`   // login username
	Password  string   `json:"password"`   // login password / app password (sensitive)
	From      string   `json:"from"`       // sender; falls back to username when empty
	UseSSL    bool     `json:"use_ssl"`    // true = implicit SSL on 465; false = STARTTLS or plaintext
	DefaultTo []string `json:"default_to"` // recipients used when a node names none
}

// EmailSettingMasked is the redacted view returned to the UI: it omits the password
// and only reports whether one is set.
type EmailSettingMasked struct {
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	Username    string   `json:"username"`
	From        string   `json:"from"`
	UseSSL      bool     `json:"use_ssl"`
	DefaultTo   []string `json:"default_to"`
	HasPassword bool     `json:"has_password"`
}

// GetEmailSetting returns the full email config, password included, for building the
// sender at runtime.
func (s *SettingService) GetEmailSetting(ctx context.Context) (EmailSetting, error) {
	var cfg EmailSetting
	row, err := s.repo.Get(ctx, domain.SettingKeyNotifyEmail)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return cfg, nil
		}
		return cfg, err
	}
	if len(row.Value) > 0 {
		if err := sonic.Unmarshal(row.Value, &cfg); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

// GetEmailSettingMasked returns the redacted email config for display in the UI.
func (s *SettingService) GetEmailSettingMasked(ctx context.Context) (EmailSettingMasked, error) {
	cfg, err := s.GetEmailSetting(ctx)
	if err != nil {
		return EmailSettingMasked{}, err
	}
	return EmailSettingMasked{
		Host:        cfg.Host,
		Port:        cfg.Port,
		Username:    cfg.Username,
		From:        cfg.From,
		UseSSL:      cfg.UseSSL,
		DefaultTo:   cfg.DefaultTo,
		HasPassword: cfg.Password != "",
	}, nil
}

// SetEmailSetting overwrites the email config. An empty incoming password keeps the
// stored one, so editing a non-secret field from the UI cannot clear the credential.
func (s *SettingService) SetEmailSetting(ctx context.Context, cfg EmailSetting) error {
	if cfg.Password == "" {
		if old, err := s.GetEmailSetting(ctx); err == nil && old.Password != "" {
			cfg.Password = old.Password
		}
	}
	value, err := sonic.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := s.repo.Upsert(ctx, domain.SettingKeyNotifyEmail, value); err != nil {
		return err
	}
	logger.L().Info("notify email setting updated", zap.String("host", cfg.Host))
	return nil
}

// ===================== Delivery channel: Feishu =====================

// FeishuSetting holds the connection parameters for the delivery nodes' Feishu
// channel (a custom bot webhook).
type FeishuSetting struct {
	WebhookURL string `json:"webhook_url"` // the custom bot's webhook URL
	Secret     string `json:"secret"`      // optional signing secret (sensitive)
}

// FeishuSettingMasked is the redacted view returned to the UI: it omits the secret
// and only reports whether one is set.
type FeishuSettingMasked struct {
	WebhookURL string `json:"webhook_url"`
	HasSecret  bool   `json:"has_secret"`
}

// GetFeishuSetting returns the full Feishu config, secret included, for building the
// sender at runtime.
func (s *SettingService) GetFeishuSetting(ctx context.Context) (FeishuSetting, error) {
	var cfg FeishuSetting
	row, err := s.repo.Get(ctx, domain.SettingKeyNotifyFeishu)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return cfg, nil
		}
		return cfg, err
	}
	if len(row.Value) > 0 {
		if err := sonic.Unmarshal(row.Value, &cfg); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

// GetFeishuSettingMasked returns the redacted Feishu config for display in the UI.
func (s *SettingService) GetFeishuSettingMasked(ctx context.Context) (FeishuSettingMasked, error) {
	cfg, err := s.GetFeishuSetting(ctx)
	if err != nil {
		return FeishuSettingMasked{}, err
	}
	return FeishuSettingMasked{
		WebhookURL: cfg.WebhookURL,
		HasSecret:  cfg.Secret != "",
	}, nil
}

// SetFeishuSetting overwrites the Feishu config. An empty incoming secret keeps the
// stored one.
func (s *SettingService) SetFeishuSetting(ctx context.Context, cfg FeishuSetting) error {
	if cfg.Secret == "" {
		if old, err := s.GetFeishuSetting(ctx); err == nil && old.Secret != "" {
			cfg.Secret = old.Secret
		}
	}
	value, err := sonic.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := s.repo.Upsert(ctx, domain.SettingKeyNotifyFeishu, value); err != nil {
		return err
	}
	logger.L().Info("notify feishu setting updated")
	return nil
}

// List returns every setting, generically, with values as stored. Note: settings
// containing credentials must not be returned to the UI through this endpoint; the
// handler layer routes sensitive keys through the redacted accessors instead.
func (s *SettingService) List(ctx context.Context) ([]*domain.Setting, error) {
	return s.repo.List(ctx)
}
