package repository

import (
	"context"
	"errors"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// AgentCatalogQueryRepository selects bounded projections, never raw source
// payloads, complete strategy bodies in a list, or complete equity/trade arrays.
type AgentCatalogQueryRepository struct{ db *gorm.DB }

func NewAgentCatalogQueryRepository(db *gorm.DB) *AgentCatalogQueryRepository {
	return &AgentCatalogQueryRepository{db: db}
}

func (r *AgentCatalogQueryRepository) List(ctx context.Context, kind string, before int64, limit int) (any, error) {
	if before < 0 || limit < 1 || limit > 101 {
		return nil, errors.New("catalog_query_limit_invalid")
	}
	q := r.db.WithContext(ctx)
	if before > 0 {
		q = q.Where("id < ?", before)
	}
	q = q.Order("id DESC").Limit(limit)
	var items []map[string]any
	switch kind {
	case "strategies":
		q = q.Model(&domain.Strategy{}).Select("id, version, SUBSTR(name,1,128) AS name, SUBSTR(description,1,512) AS description, period, updated_at")
	case "news":
		q = q.Model(&domain.News{}).Select("id, source, SUBSTR(title,1,512) AS title, SUBSTR(url,1,512) AS url, published_at")
	case "cost_models":
		q = q.Model(&domain.Cost{}).Select("id, SUBSTR(name,1,64) AS name, is_default, commission_rate, min_commission, stamp_duty_rate, slippage_bps")
	case "backtests":
		q = q.Model(&domain.BacktestJob{}).Select("id, strategy_id, cost_id, initial_capital, start_date, end_date, status, created_at, finished_at")
	default:
		return nil, errors.New("catalog_query_kind_invalid")
	}
	err := q.Find(&items).Error
	if err != nil {
		logger.L().Error("agent catalog list failed", zap.Error(err))
		return nil, err
	}
	if items == nil {
		items = []map[string]any{}
	}
	return items, nil
}

func (r *AgentCatalogQueryRepository) Get(ctx context.Context, kind string, id int64) (any, error) {
	if id < 1 {
		return nil, errors.New("catalog_query_id_invalid")
	}
	q := r.db.WithContext(ctx)
	// SUBSTR caps the allocation even if legacy product data exceeds the tool's
	// limits. Raw JSON is checked before it can be marshaled to a model.
	var item map[string]any
	switch kind {
	case "strategy":
		var st domain.Strategy
		err := q.Select("id,version,SUBSTR(name,1,128) AS name,SUBSTR(description,1,512) AS description,SUBSTR(universe,1,8193) AS universe,period,SUBSTR(body,1,65537) AS body,created_at,updated_at").First(&st, id).Error
		if err != nil {
			return nil, err
		}
		if len(st.Body) > 65536 {
			return nil, errors.New("catalog_record_too_large")
		}
		return &st, nil
	case "news":
		q = q.Model(&domain.News{}).Select("id,source,SUBSTR(title,1,512) AS title,SUBSTR(content,1,8192) AS content,SUBSTR(url,1,512) AS url,published_at,CASE WHEN LENGTH(content)>8192 THEN 1 ELSE 0 END AS content_truncated").Where("id = ?", id)
	case "event":
		q = q.Model(&domain.Event{}).Select("id,event_uid,pipeline_id,source,status,received_at,finished_at,SUBSTR(final_payload,1,8192) AS final_payload_text,CASE WHEN LENGTH(final_payload)>8192 THEN 1 ELSE 0 END AS payload_truncated").Where("id = ?", id)
	case "backtest_job":
		q = q.Model(&domain.BacktestJob{}).Select("id,strategy_id,cost_id,initial_capital,start_date,end_date,status,created_at,started_at,finished_at").Where("id = ?", id)
	case "backtest_report":
		q = q.Model(&domain.BacktestResult{}).Select("job_id,total_return,annualized_return,max_drawdown,sharpe,win_rate,trade_count,created_at").Where("job_id = ?", id)
	default:
		return nil, errors.New("catalog_query_kind_invalid")
	}
	if err := q.Take(&item).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			logger.L().Error("agent catalog get failed", zap.Error(err))
		}
		return nil, err
	}
	// SUBSTR over a payload stored as bytes may be scanned as []byte (including
	// SQLite BLOBs and MySQL text expressions). The public projection is text;
	// marshaling the driver bytes directly would silently turn it into base64.
	if payload, ok := item["final_payload_text"].([]byte); ok {
		item["final_payload_text"] = string(payload)
	}
	return item, nil
}
