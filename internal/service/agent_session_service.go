package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/agentbridge"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

var (
	ErrAgentSessionInput       = errors.New("agent_session_input_invalid")
	ErrAgentSessionNotFound    = errors.New("agent_session_not_found")
	ErrAgentSessionConflict    = errors.New("agent_idempotency_conflict")
	ErrAgentSessionState       = errors.New("agent_session_not_active")
	ErrAgentSessionUnavailable = errors.New("agent_session_runtime_unavailable")
	ErrAgentSessionStore       = errors.New("agent_session_storage_invalid")
	ErrAgentSessionProfile     = errors.New("agent_profile_unavailable")
	ErrAgentSessionModel       = errors.New("agent_model_not_ready")
)
var sessionIDPattern = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
var sessionKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
var sessionActorPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
var sessionProfilePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

type AgentSessionCreate struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Profile  string `json:"profile"`
	Title    string `json:"title"`
}
type AgentSessionPatch struct {
	Title    *string `json:"title,omitempty"`
	Archived *bool   `json:"archived,omitempty"`
}
type AgentSessionView struct {
	SessionID                  string     `json:"session_id"`
	Provider                   string     `json:"provider"`
	Model                      string     `json:"model"`
	Profile                    string     `json:"profile"`
	Title                      string     `json:"title"`
	TitlePending               bool       `json:"title_pending"`
	TitleRevision              uint64     `json:"title_revision"`
	Status                     string     `json:"status"`
	CreatedProfileRevision     string     `json:"created_profile_revision"`
	CreatedModelConfigRevision string     `json:"created_model_config_revision"`
	ProvisioningErrorCode      string     `json:"provisioning_error_code,omitempty"`
	CreatedAt                  time.Time  `json:"created_at"`
	UpdatedAt                  time.Time  `json:"updated_at"`
	ArchivedAt                 *time.Time `json:"archived_at,omitempty"`
}
type AgentSessionPage struct {
	Items      []AgentSessionView `json:"items"`
	NextCursor *string            `json:"next_cursor"`
}
type AgentSessionService struct {
	repo *repository.AgentSessionRepository
}

func NewAgentSessionService(repo *repository.AgentSessionRepository) *AgentSessionService {
	return &AgentSessionService{repo: repo}
}
func sessionDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func sessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	now := uint64(time.Now().UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(now)
		now >>= 8
	}
	n := new(big.Int).SetBytes(b[:])
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var out [26]byte
	for i := 25; i >= 0; i-- {
		out[i] = alphabet[n.Uint64()&31]
		n.Rsh(n, 5)
	}
	return string(out[:]), nil
}
func normalizeSessionCreate(r AgentSessionCreate) (AgentSessionCreate, string, error) {
	r.Title = strings.TrimSpace(r.Title)
	if !modelProviderPattern.MatchString(r.Provider) || !modelIDPattern.MatchString(r.Model) || !sessionProfilePattern.MatchString(r.Profile) || !validSessionTitle(r.Title) {
		return r, "", ErrAgentSessionInput
	}
	data, err := sonic.Marshal(r)
	if err != nil {
		return r, "", ErrAgentSessionInput
	}
	return r, "sha256:" + sessionDigest(string(data)), nil
}
func validSessionTitle(s string) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= 256 && !strings.ContainsAny(s, "\x00\r\n")
}
func SessionView(r domain.AgentSessionBinding) AgentSessionView {
	return AgentSessionView{SessionID: r.ID, Provider: r.Provider, Model: r.Model, Profile: r.Profile, Title: r.Title,
		TitlePending: r.TitleGeneration > r.TitleSettledGeneration, TitleRevision: r.TitleGeneration + r.TitleSettledGeneration,
		Status: r.Status, CreatedProfileRevision: r.CreatedProfileRevision, CreatedModelConfigRevision: r.CreatedModelConfigRevision,
		ProvisioningErrorCode: r.ProvisioningErrorCode, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ArchivedAt: r.ArchivedAt}
}
func sessionStoreResult(row domain.AgentSessionBinding, err error) (domain.AgentSessionBinding, error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, ErrAgentSessionNotFound
	}
	return row, err
}
func (s *AgentSessionService) Get(ctx context.Context, actor, id string) (domain.AgentSessionBinding, error) {
	if !sessionActorPattern.MatchString(actor) || !sessionIDPattern.MatchString(id) {
		return domain.AgentSessionBinding{}, ErrAgentSessionInput
	}
	row, err := s.repo.Get(ctx, actor, id)
	return sessionStoreResult(row, err)
}

// Find checks only the client's immutable input, before consulting today's model
// or Profile revision. Metadata edits and configuration rotation preserve retries.
func (s *AgentSessionService) Find(ctx context.Context, actor, key string, request AgentSessionCreate) (domain.AgentSessionBinding, error) {
	if !sessionActorPattern.MatchString(actor) || !sessionKeyPattern.MatchString(key) {
		return domain.AgentSessionBinding{}, ErrAgentSessionInput
	}
	_, hash, err := normalizeSessionCreate(request)
	if err != nil {
		return domain.AgentSessionBinding{}, err
	}
	row, err := s.repo.GetByKey(ctx, actor, sessionDigest(key))
	if err != nil {
		return sessionStoreResult(row, err)
	}
	if row.CreateRequestHash != hash {
		return row, ErrAgentSessionConflict
	}
	return row, nil
}
func (s *AgentSessionService) Prepare(ctx context.Context, actor, key string, request AgentSessionCreate, profileRevision, modelRevision string) (domain.AgentSessionBinding, error) {
	if !sessionActorPattern.MatchString(actor) || !sessionKeyPattern.MatchString(key) || !modelRevisionPattern.MatchString(modelRevision) {
		return domain.AgentSessionBinding{}, ErrAgentSessionInput
	}
	request, hash, err := normalizeSessionCreate(request)
	if err != nil {
		return domain.AgentSessionBinding{}, err
	}
	id, err := sessionID()
	if err != nil {
		return domain.AgentSessionBinding{}, err
	}
	bridge := agentbridge.SessionCreate{SessionID: id, Provider: request.Provider, Model: request.Model, Profile: request.Profile, ProfileRevision: profileRevision, ModelConfigRevision: modelRevision}
	provisionHash, err := agentbridge.ProvisionHash(bridge)
	if err != nil {
		return domain.AgentSessionBinding{}, ErrAgentSessionInput
	}
	row, err := s.repo.InsertOrGet(ctx, domain.AgentSessionBinding{ID: id, ActorID: actor, ProvisionRequestKey: sessionDigest(key), CreateRequestHash: hash, ProvisionRequestHash: provisionHash, Status: domain.AgentSessionProvisioning, Title: request.Title, Profile: request.Profile, Provider: request.Provider, Model: request.Model, CreatedProfileRevision: profileRevision, CreatedModelConfigRevision: modelRevision})
	if err == nil && row.CreateRequestHash != hash {
		return row, ErrAgentSessionConflict
	}
	if err == nil {
		logger.L().Info("agent session prepared", zap.String("session_id", row.ID), zap.String("status", row.Status))
	}
	return row, err
}
func SessionBridgeRequest(r domain.AgentSessionBinding) agentbridge.SessionCreate {
	return agentbridge.SessionCreate{SessionID: r.ID, Provider: r.Provider, Model: r.Model, Profile: r.Profile, ProfileRevision: r.CreatedProfileRevision, ModelConfigRevision: r.CreatedModelConfigRevision, ProvisionRequestHash: r.ProvisionRequestHash}
}
func (s *AgentSessionService) Claim(ctx context.Context, row domain.AgentSessionBinding) (string, bool, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", false, err
	}
	attempt := hex.EncodeToString(b[:])
	claimed, err := s.repo.Claim(ctx, row.ActorID, row.ID, attempt, time.Now().UnixMilli())
	return attempt, claimed, err
}
func (s *AgentSessionService) Complete(ctx context.Context, row domain.AgentSessionBinding, attempt string, result *agentbridge.SessionCreated, code string) error {
	if result == nil {
		switch code {
		case "agent_session_runtime_unavailable", "agent_session_unbound", "agent_session_storage_missing", "agent_session_conflict", "agent_configuration_conflict":
		default:
			return ErrAgentSessionStore
		}
	}
	values := map[string]any{"lease_until_ms": 0, "provision_attempt": "", "retry_at_ms": time.Now().UnixMilli() + 60000}
	if result == nil {
		values["status"] = domain.AgentSessionFailed
		values["provisioning_error_code"] = code
	} else {
		if result.SessionID != row.ID || result.DSHSessionID != row.ID || !result.Durable || result.ProvisionRequestHash != row.ProvisionRequestHash || result.Provider != row.Provider || result.Model != row.Model || result.Profile != row.Profile || result.CreatedModelConfigRevision != row.CreatedModelConfigRevision || result.CreatedProfileRevision != row.CreatedProfileRevision {
			return ErrAgentSessionStore
		}
		p := result.RuntimeProvenance
		if p.BridgeProtocol != 1 || p.SessionFormat != 0 || p.EventJournalFormat != 1 || p.SessionBindingFormat != 1 || (p.Backend != "cordis" && p.Backend != "acp") || p.AdapterVersion == "" || len(p.AdapterVersion) > 128 || p.DSHVersion == "" || len(p.DSHVersion) > 128 {
			return ErrAgentSessionStore
		}
		data, err := sonic.Marshal(result.RuntimeProvenance)
		if err != nil {
			return err
		}
		values["status"] = domain.AgentSessionActive
		values["dsh_session_id"] = result.DSHSessionID
		values["runtime_provenance_json"] = string(data)
		values["provisioning_error_code"] = ""
		values["retry_at_ms"] = 0
	}
	updated, err := s.repo.Finish(ctx, row.ActorID, row.ID, attempt, values)
	if err == nil && updated {
		logger.L().Info("agent session provisioning settled", zap.String("session_id", row.ID), zap.String("status", values["status"].(string)))
	}
	return err
}
func (s *AgentSessionService) List(ctx context.Context, actor, cursor string, limit int) (AgentSessionPage, error) {
	if !sessionActorPattern.MatchString(actor) || (cursor != "" && !sessionIDPattern.MatchString(cursor)) || limit < 1 || limit > 100 {
		return AgentSessionPage{}, ErrAgentSessionInput
	}
	rows, err := s.repo.List(ctx, actor, cursor, limit+1)
	if err != nil {
		return AgentSessionPage{}, err
	}
	page := AgentSessionPage{Items: []AgentSessionView{}}
	if len(rows) > limit {
		next := rows[limit-1].ID
		page.NextCursor = &next
		rows = rows[:limit]
	}
	for _, row := range rows {
		page.Items = append(page.Items, SessionView(row))
	}
	return page, nil
}
func (s *AgentSessionService) Patch(ctx context.Context, actor, id string, patch AgentSessionPatch) (AgentSessionView, error) {
	row, err := s.Get(ctx, actor, id)
	if err != nil {
		return AgentSessionView{}, err
	}
	if patch.Title == nil && patch.Archived == nil {
		return AgentSessionView{}, ErrAgentSessionInput
	}
	values := map[string]any{}
	if patch.Title != nil {
		title := strings.TrimSpace(*patch.Title)
		if !validSessionTitle(title) {
			return AgentSessionView{}, ErrAgentSessionInput
		}
		values["title"] = title
	}
	if patch.Archived != nil {
		if row.Status != domain.AgentSessionActive && row.Status != domain.AgentSessionArchived {
			return AgentSessionView{}, ErrAgentSessionState
		}
		values["status"] = domain.AgentSessionActive
		values["archived_at"] = nil
		if *patch.Archived {
			values["status"] = domain.AgentSessionArchived
			if row.ArchivedAt != nil {
				values["archived_at"] = row.ArchivedAt
			} else {
				values["archived_at"] = time.Now().UTC().Truncate(time.Millisecond)
			}
		}
	}
	if err = s.repo.Patch(ctx, actor, id, values, patch.Archived != nil); err != nil {
		return AgentSessionView{}, err
	}
	row, err = s.Get(ctx, actor, id)
	if err == nil {
		logger.L().Info("agent session metadata updated", zap.String("session_id", id), zap.String("status", row.Status))
	}
	return SessionView(row), err
}
func (s *AgentSessionService) Due(ctx context.Context) ([]domain.AgentSessionBinding, error) {
	return s.repo.Due(ctx, 20)
}
