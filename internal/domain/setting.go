package domain

import (
	"encoding/json"
	"time"
)

// Setting key constants. Register a new setting here; the shape of its value is
// interpreted by the corresponding service accessor.
const (
	// SettingKeyLLMProviders stores the map of LLM providers available to the
	// pipeline's AI nodes; the value is JSON for map[string]LLMProvider. It is
	// maintained from the UI, and the client is resolved by provider name at
	// runtime.
	SettingKeyLLMProviders = "llm.providers"

	// SettingKeyNewsSources stores per-source collection settings as
	// map[string]NewsSourceSetting, JSON-encoded. It is maintained from the UI and
	// re-read by the collector every round, so sources can be enabled, disabled or
	// re-timed live.
	SettingKeyNewsSources = "news.sources"

	// SettingKeyNewsCollector stores the collector's global config
	// (NewsCollectorConfig: the master switch plus the base poll tick), JSON-encoded.
	// The collector re-reads it every round, so it can be started, stopped and
	// re-timed at runtime without a restart.
	SettingKeyNewsCollector = "news.collector"

	// SettingKeyNotifyEmail stores the email (SMTP) delivery channel config for
	// delivery nodes, JSON-encoded. It is maintained from the UI and read per
	// channel at runtime, so UI changes take effect immediately.
	SettingKeyNotifyEmail = "notify.email"

	// SettingKeyNotifyFeishu stores the Feishu (custom bot webhook) delivery
	// channel config for delivery nodes, JSON-encoded. It is maintained from the
	// UI and read per channel at runtime.
	SettingKeyNotifyFeishu = "notify.feishu"
)

// Setting is one row of the generic key-value settings table. Value is JSON text
// carrying arbitrary structure, so settings can evolve with the product without
// adding tables or columns.
type Setting struct {
	Key       string          `json:"key"        gorm:"primaryKey;size:128"`
	Value     json.RawMessage `json:"value"      gorm:"type:text"`
	UpdatedAt time.Time       `json:"updated_at"`
}

func (Setting) TableName() string { return "setting" }
