package repository

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/utils/strictjson"
	"github.com/quant4dad/internal/utils/tooljson"
)

var ErrAgentReleaseInvalid = errors.New("agent_release_invalid")
var releaseVersionPattern = regexp.MustCompile(`^[A-Za-z0-9_.+-]{1,128}$`)
var releaseDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Read one atomically published operator file. No network discovery, unsigned
// image tags, or runtime-supplied claims may change Go's execution authority.
type AgentReleaseRepository struct {
	path       string
	key        ed25519.PublicKey
	q4dVersion string
	allowEdge  bool
}

func NewAgentReleaseRepository(path, publicKey, q4dVersion string, allowEdge bool) *AgentReleaseRepository {
	key, _ := base64.RawURLEncoding.Strict().DecodeString(publicKey)
	return &AgentReleaseRepository{path: path, key: ed25519.PublicKey(key), q4dVersion: q4dVersion, allowEdge: allowEdge}
}

func (r *AgentReleaseRepository) Current(ctx context.Context) (config.AgentRunManifest, error) {
	var zero config.AgentRunManifest
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	actual, err := filepath.EvalSymlinks(r.path)
	if err != nil || !filepath.IsAbs(r.path) || filepath.Clean(r.path) != r.path || actual != r.path || len(r.key) != ed25519.PublicKeySize {
		return zero, ErrAgentReleaseInvalid
	}
	f, err := os.OpenFile(r.path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return zero, ErrAgentReleaseInvalid
	}
	defer f.Close()
	s, err := f.Stat()
	if err != nil || !s.Mode().IsRegular() || s.Mode().Perm()&0022 != 0 || s.Size() > 65536 {
		return zero, ErrAgentReleaseInvalid
	}
	body, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(body) > 65536 {
		return zero, ErrAgentReleaseInvalid
	}
	var pointer map[string]json.RawMessage
	if strictjson.DecodeNullable(body, &pointer, 65536) != nil {
		return zero, ErrAgentReleaseInvalid
	}
	var payload map[string]json.RawMessage
	if sonic.Unmarshal(pointer["release"], &payload) != nil || payload == nil {
		return zero, ErrAgentReleaseInvalid
	}
	var signature string
	if sonic.Unmarshal(payload["signature"], &signature) != nil {
		return zero, ErrAgentReleaseInvalid
	}
	delete(payload, "signature")
	encoded, err := sonic.Marshal(payload)
	if err != nil {
		return zero, ErrAgentReleaseInvalid
	}
	canonical, err := tooljson.Canonical(encoded)
	sig, decodeErr := base64.RawURLEncoding.Strict().DecodeString(signature)
	if err != nil || decodeErr != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(r.key, canonical, sig) {
		return zero, ErrAgentReleaseInvalid
	}
	var release struct {
		SchemaVersion        int    `json:"schema_version"`
		Q4DVersion           string `json:"q4d_version"`
		Version              string `json:"version"`
		Channel              string `json:"channel"`
		Image                string `json:"image"`
		AdapterVersion       string `json:"adapter_version"`
		DSHVersion           string `json:"dsh_version"`
		BridgeProtocol       int    `json:"bridge_protocol"`
		SessionFormat        int    `json:"session_format"`
		EventJournalFormat   int    `json:"event_journal_format"`
		SessionBindingFormat int    `json:"session_binding_format"`
	}
	if sonic.Unmarshal(encoded, &release) != nil || release.SchemaVersion != 1 || release.Q4DVersion != r.q4dVersion ||
		(release.Channel != "verified" && !(r.allowEdge && release.Channel == "edge")) || release.BridgeProtocol != 1 || release.DSHVersion != "0.1.2-alpha.5" ||
		release.SessionFormat != 0 || release.EventJournalFormat != 1 || release.SessionBindingFormat != 1 {
		return zero, ErrAgentReleaseInvalid
	}
	_, imageDigest, ok := strings.Cut(release.Image, "@")
	if !ok || !releaseDigestPattern.MatchString(imageDigest) ||
		!releaseVersionPattern.MatchString(release.Version) || !releaseVersionPattern.MatchString(release.AdapterVersion) {
		return zero, ErrAgentReleaseInvalid
	}
	return config.AgentRunManifest{Q4DVersion: release.Q4DVersion, AgentImageDigest: imageDigest, AgentRuntimeVersion: release.Version,
		AdapterVersion: release.AdapterVersion, DSHVersion: release.DSHVersion}, nil
}
