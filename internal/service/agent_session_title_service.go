package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"go.uber.org/zap"
)

func (s *AgentSessionService) DueTitles(ctx context.Context) ([]domain.AgentSessionBinding, error) {
	return s.repo.DueTitles(ctx, time.Now().UnixMilli())
}

func (s *AgentSessionService) ClaimTitle(ctx context.Context, row domain.AgentSessionBinding) (string, bool, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", false, err
	}
	attempt := hex.EncodeToString(b[:])
	ok, err := s.repo.ClaimTitle(ctx, row, attempt, time.Now().UnixMilli())
	return attempt, ok, err
}

func (s *AgentSessionService) FinishTitle(ctx context.Context, row domain.AgentSessionBinding, attempt, title string) (bool, error) {
	if title != "" && NormalizeAgentSessionTitle(title) != title {
		return false, ErrAgentSessionInput
	}
	updated, err := s.repo.FinishTitle(ctx, row, attempt, title)
	if err == nil && updated {
		logger.L().Info("agent session title settled", zap.String("session_id", row.ID), zap.Uint64("generation", row.TitleGeneration), zap.Bool("generated", title != ""))
	}
	return updated, err
}

// Treat model output as plain display text, never an instruction or Markdown.
// Reject explanations, reasoning and malformed output instead of storing them.
func NormalizeAgentSessionTitle(text string) string {
	if !utf8.ValidString(text) {
		return ""
	}
	title := strings.TrimSpace(text)
	title = strings.TrimSpace(strings.Trim(title, "\"'“”‘’"))
	if title == "" || utf8.RuneCountInString(title) > 48 || strings.ContainsAny(title, "\r\n`<>[]{}") {
		return ""
	}
	for _, r := range title {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ""
		}
	}
	return title
}
