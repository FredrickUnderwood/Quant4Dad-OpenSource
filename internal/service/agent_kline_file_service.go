package service

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/pythonexec"
	"github.com/quant4dad/internal/repository"
)

const KlineFilePageSize = 50
const KlineFileMaxPageSize = 100

var klineFileID = regexp.MustCompile(`^[0-9a-f]{64}$`)

type AgentKlineFileService struct {
	files  *repository.AgentToolArtifactRepository
	python pythonexec.Executor
}

func NewAgentKlineFileService(files *repository.AgentToolArtifactRepository, python ...pythonexec.Executor) *AgentKlineFileService {
	s := &AgentKlineFileService{files: files}
	if len(python) > 0 {
		s.python = python[0]
	}
	return s
}

func (s *AgentKlineFileService) PythonAvailable() bool { return s.python != nil }

type storedKlineFile struct {
	Actor     string           `json:"actor"`
	Session   string           `json:"session"`
	ExpiresAt time.Time        `json:"expires_at"`
	Result    QueryKlineResult `json:"result"`
}
type KlineFileDescriptor struct {
	FileID           string `json:"file_id"`
	File             string `json:"file"`
	Code             string `json:"code"`
	Period           string `json:"period"`
	Count            int    `json:"count"`
	FirstDate        string `json:"first_date"`
	LastDate         string `json:"last_date"`
	Truncated        bool   `json:"truncated"`
	DataAsOf         string `json:"data_as_of"`
	ExpiresAt        string `json:"expires_at"`
	PageSize         int    `json:"page_size"`
	PriceBasis       string `json:"price_basis"`
	AdjustmentStatus string `json:"adjustment_status"`
	AdjustmentSource string `json:"adjustment_source"`
}
type KlineFilePage struct {
	FileID           string   `json:"file_id"`
	Code             string   `json:"code"`
	Period           string   `json:"period"`
	Offset           int      `json:"offset"`
	Count            int      `json:"count"`
	Total            int      `json:"total"`
	NextOffset       int      `json:"next_offset"`
	HasMore          bool     `json:"has_more"`
	Columns          []string `json:"columns"`
	Rows             [][]any  `json:"rows"`
	PriceBasis       string   `json:"price_basis"`
	AdjustmentStatus string   `json:"adjustment_status"`
	AdjustmentSource string   `json:"adjustment_source"`
}

func (s *AgentKlineFileService) Save(ctx context.Context, result QueryKlineResult) (KlineFileDescriptor, error) {
	var out KlineFileDescriptor
	execution, ok := AgentExecutionFromContext(ctx)
	if !ok || execution.Audit.ActorID == "" || execution.Audit.SessionID == "" || result.Count != len(result.Bars) || result.Count > 500 {
		return out, ErrToolInput
	}
	expires := time.Now().UTC().Add(24 * time.Hour)
	body, err := sonic.Marshal(storedKlineFile{execution.Audit.ActorID, execution.Audit.SessionID, expires, result})
	if err != nil {
		return out, ErrToolUnavailable
	}
	ref, err := s.files.Put(body)
	if err != nil {
		return out, ErrToolUnavailable
	}
	out = KlineFileDescriptor{FileID: strings.TrimSuffix(ref, ".json"), File: "tmp/kline/" + ref,
		Code: result.Code, Period: result.Period, Count: result.Count, Truncated: result.Truncated,
		DataAsOf: result.DataAsOf, ExpiresAt: expires.Format(time.RFC3339), PageSize: KlineFilePageSize}
	out.PriceBasis = "stored_ohlc"
	out.AdjustmentStatus, out.AdjustmentSource = factorProvenance(result.Period, result.Bars)
	if result.Count > 0 {
		out.FirstDate = result.Bars[0].Date.Format("2006-01-02")
		out.LastDate = result.Bars[result.Count-1].Date.Format("2006-01-02")
	}
	return out, nil
}

// Read accepts an opaque ID, never a model-supplied filesystem path. A later
// Run in the same Session can resume reading until expiration.
func (s *AgentKlineFileService) Read(ctx context.Context, id string, offset, limit int) (KlineFilePage, error) {
	var out KlineFilePage
	if offset < 0 || offset > 500 || limit < 1 || limit > KlineFileMaxPageSize {
		return out, ErrToolInput
	}
	file, err := s.load(ctx, id)
	if err != nil {
		return out, err
	}
	if offset > file.Result.Count {
		return out, ErrToolInput
	}
	end := min(offset+limit, file.Result.Count)
	out = KlineFilePage{FileID: id, Code: file.Result.Code, Period: file.Result.Period, Offset: offset,
		Count: end - offset, Total: file.Result.Count, NextOffset: end, HasMore: end < file.Result.Count,
		Columns: []string{"date", "open", "high", "low", "close", "volume", "amount", "adj_factor"}, Rows: [][]any{}}
	out.PriceBasis = "stored_ohlc"
	out.AdjustmentStatus, out.AdjustmentSource = factorProvenance(file.Result.Period, file.Result.Bars)
	for _, bar := range file.Result.Bars[offset:end] {
		out.Rows = append(out.Rows, []any{bar.Date.Format("2006-01-02"), bar.Open, bar.High, bar.Low, bar.Close, bar.Volume, bar.Amount, bar.AdjFactor})
	}
	return out, nil
}

func (s *AgentKlineFileService) load(ctx context.Context, id string) (storedKlineFile, error) {
	var file storedKlineFile
	execution, ok := AgentExecutionFromContext(ctx)
	if !ok || execution.Audit.ActorID == "" || execution.Audit.SessionID == "" || !klineFileID.MatchString(id) {
		return file, ErrToolInput
	}
	body, err := s.files.Get(id + ".json")
	if err != nil {
		return file, ErrToolUnavailable
	}
	if sonic.Unmarshal(body, &file) != nil || file.Actor != execution.Audit.ActorID || file.Session != execution.Audit.SessionID ||
		!time.Now().Before(file.ExpiresAt) || file.Result.Count != len(file.Result.Bars) || file.Result.Count < 0 || file.Result.Count > 500 {
		return storedKlineFile{}, ErrToolUnavailable
	}
	return file, nil
}
func (s *AgentKlineFileService) Maintain(ctx context.Context, now time.Time) (int, error) {
	return s.files.Sweep(ctx, now, func(context.Context, string) (bool, error) { return false, nil })
}
