package domain

import (
	"encoding/json"
	"time"
)

// The news source identifier (News.Source / NewsSubscription.Source) is not a
// fixed enum: it is exactly the Name() of a datasource.NewsSource implementation.
// The set of usable names is decided by the registry at startup and handed to the
// service layer for subscription validation via service.SetKnownNewsSources. See
// internal/repository/datasource/README.md for bundled and custom providers.

// News is one collected, stored news item — the single source of truth for
// deduplication and chronological browsing. When it matches a subscription, the
// collector reuses PipelineApp.Ingest to create a pipeline_event. The two are
// decoupled: one news item can trigger several pipelines while the news table
// stores it once.
type News struct {
	ID          int64           `json:"id"           gorm:"primaryKey;autoIncrement"`
	Source      string          `json:"source"       gorm:"size:32;not null;uniqueIndex:uk_news_source_ext,priority:1;index:idx_news_source_time,priority:1"`
	ExternalID  string          `json:"external_id"  gorm:"size:128;not null;uniqueIndex:uk_news_source_ext,priority:2"` // the source's own id; the dedup key
	Title       string          `json:"title"        gorm:"size:512"`
	Content     string          `json:"content"      gorm:"type:text"`
	URL         string          `json:"url"          gorm:"size:512"`
	PublishedAt time.Time       `json:"published_at" gorm:"index:idx_news_source_time,priority:2;index:idx_news_published"` // chronological browsing
	Raw         json.RawMessage `json:"raw"          gorm:"type:text"`                                                      // the original item, for auditing and re-parsing
	CreatedAt   time.Time       `json:"created_at"`
}

func (News) TableName() string { return "news" }

// NewsSubscription expresses "this pipeline subscribes to this news source".
// There are no foreign keys; the association is maintained in the business layer.
// The unique index on (pipeline_id, source) prevents duplicate subscriptions.
type NewsSubscription struct {
	ID         int64     `json:"id"          gorm:"primaryKey;autoIncrement"`
	PipelineID int64     `json:"pipeline_id" gorm:"not null;uniqueIndex:uk_news_sub_pipe_src,priority:1;index"`
	Source     string    `json:"source"      gorm:"size:32;not null;uniqueIndex:uk_news_sub_pipe_src,priority:2;index:idx_news_sub_source"`
	CreatedAt  time.Time `json:"created_at"`
}

func (NewsSubscription) TableName() string { return "news_subscription" }
