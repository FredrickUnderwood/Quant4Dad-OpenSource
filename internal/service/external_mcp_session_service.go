package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"slices"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/agentrunauth"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
	"gorm.io/gorm"
)

var ErrExternalMCPSession = errors.New("external_mcp_session_unavailable")
var ErrExternalMCPConfiguration = errors.New("external_mcp_configuration_invalid")
var externalMCPSessionID = regexp.MustCompile(`^[0-9a-f]{32}$`)

type ExternalMCPSessionService struct {
	repo         *repository.ExternalMCPSessionRepository
	ttl          time.Duration
	maxToolCalls int64
}

// The persisted authority is non-secret provenance, never a signed Agent token.
type externalMCPAuthority struct {
	AuthorizationMode string              `json:"authorization_mode"`
	Claims            agentrunauth.Claims `json:"claims"`
}

func NewExternalMCPSessionService(repo *repository.ExternalMCPSessionRepository, ttl time.Duration, maxToolCalls int64) (*ExternalMCPSessionService, error) {
	if repo == nil || ttl < time.Minute || ttl > 24*time.Hour || maxToolCalls < 1 || maxToolCalls > 10000 {
		return nil, ErrExternalMCPConfiguration
	}
	return &ExternalMCPSessionService{repo, ttl, maxToolCalls}, nil
}

func (s *ExternalMCPSessionService) Create(ctx context.Context, revision string, names []string, tokenAuthorized bool) (domain.ExternalMCPSession, error) {
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return domain.ExternalMCPSession{}, err
	}
	session, run := hex.EncodeToString(entropy[:16]), hex.EncodeToString(entropy[16:])
	now := time.Now().UTC().Truncate(time.Second)
	mode := "approval_required"
	if tokenAuthorized {
		mode = "token_authorized"
	}
	claims := agentrunauth.Claims{
		Issuer: domain.ExternalMCPActor, SessionID: session, IssuedAt: now.Unix(), ExpiresAt: now.Add(s.ttl).Unix(), AllowedTools: slices.Clone(names),
		Envelope: agentrunauth.Envelope{RunID: run, ActorID: domain.ExternalMCPActor, ProductProfile: domain.ExternalMCPProfile, ToolCatalogRevision: revision,
			Budgets: agentrunauth.Budgets{MaxToolCalls: s.maxToolCalls, WallTimeMS: s.ttl.Milliseconds()}},
	}
	authority := externalMCPAuthority{AuthorizationMode: mode, Claims: claims}
	digest, err := externalMCPAuthorityDigest(authority)
	if err != nil {
		return domain.ExternalMCPSession{}, err
	}
	authority.Claims.EnvelopeDigest = digest
	body, err := sonic.MarshalString(authority)
	if err != nil {
		return domain.ExternalMCPSession{}, err
	}
	row := domain.AgentRequestBinding{ID: run, SessionID: session, ActorID: domain.ExternalMCPActor, ClientRequestKey: session,
		RequestHash: digest, EnvelopeDigest: digest, ClaimsJSON: body, SigningKeyID: domain.ExternalMCPSigningKey, DeadlineMS: claims.DeadlineMillis()}
	if err := s.repo.Create(ctx, row); err != nil {
		return domain.ExternalMCPSession{}, err
	}
	return externalMCPView(authority), nil
}

func externalMCPAuthorityDigest(authority externalMCPAuthority) (string, error) {
	authority.Claims.EnvelopeDigest = ""
	body, err := sonic.Config{SortMapKeys: true}.Froze().Marshal(authority)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func externalMCPView(authority externalMCPAuthority) domain.ExternalMCPSession {
	return domain.ExternalMCPSession{ID: authority.Claims.SessionID, ExpiresAt: time.UnixMilli(authority.Claims.DeadlineMillis()).UTC(), MaxToolCalls: authority.Claims.Envelope.Budgets.MaxToolCalls, AuthorizationMode: authority.AuthorizationMode}
}

func (s *ExternalMCPSessionService) Authorize(ctx context.Context, session, revision string, names []string, tokenAuthorized bool) (agentrunauth.Claims, domain.ExternalMCPSession, error) {
	rejected := func() (agentrunauth.Claims, domain.ExternalMCPSession, error) {
		return agentrunauth.Claims{}, domain.ExternalMCPSession{}, ErrExternalMCPSession
	}
	if !externalMCPSessionID.MatchString(session) {
		return rejected()
	}
	row, err := s.repo.Get(ctx, session)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return rejected()
	}
	if err != nil {
		return agentrunauth.Claims{}, domain.ExternalMCPSession{}, err
	}
	var authority externalMCPAuthority
	if len(row.ClaimsJSON) > 65536 || sonic.UnmarshalString(row.ClaimsJSON, &authority) != nil {
		return rejected()
	}
	c := authority.Claims
	mode := "approval_required"
	if tokenAuthorized {
		mode = "token_authorized"
	}
	digest, err := externalMCPAuthorityDigest(authority)
	if err != nil || row.Revoked || row.DeadlineMS <= time.Now().UnixMilli() || row.DeadlineMS != c.DeadlineMillis() || row.ActorID != domain.ExternalMCPActor || row.ID != c.Envelope.RunID || row.SessionID != c.SessionID ||
		c.Envelope.ActorID != domain.ExternalMCPActor || c.Envelope.ProductProfile != domain.ExternalMCPProfile || c.Issuer != domain.ExternalMCPActor || c.Envelope.ToolCatalogRevision != revision ||
		c.Envelope.Budgets.MaxToolCalls < 1 || c.Envelope.Budgets.MaxToolCalls > s.maxToolCalls || c.Envelope.Budgets.WallTimeMS < 1 || c.Envelope.Budgets.WallTimeMS > s.ttl.Milliseconds() ||
		digest != row.EnvelopeDigest || digest != c.EnvelopeDigest || digest != row.RequestHash || authority.AuthorizationMode != mode || !slices.Equal(c.AllowedTools, names) {
		return rejected()
	}
	return c, externalMCPView(authority), nil
}

func (s *ExternalMCPSessionService) Revoke(ctx context.Context, session string) error {
	if !externalMCPSessionID.MatchString(session) {
		return ErrExternalMCPSession
	}
	if _, err := s.repo.Get(ctx, session); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrExternalMCPSession
		}
		return err
	}
	return s.repo.Revoke(ctx, session)
}
