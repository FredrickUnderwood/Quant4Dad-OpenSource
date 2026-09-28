// Package agentrunauth implements the Q4D Run Capability v1 signing and
// verification boundary. Live authorization remains the control plane's job.
package agentrunauth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bytedance/sonic"
)

const (
	RuntimeAudience = "q4d-agent-runtime"
	GatewayAudience = "q4d-internal-mcp"
	TokenType       = "q4d-run-capability-v1+jwt"
	MaxTokenBytes   = 8192
)

var (
	ErrConfiguration = errors.New("agent_capability_configuration_invalid")
	ErrInvalid       = errors.New("agent_capability_input_invalid")
	ErrRejected      = errors.New("agent_capability_rejected")
	ErrExpired       = errors.New("agent_capability_expired")
	id               = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	hash             = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	version          = regexp.MustCompile(`^[A-Za-z0-9_.+-]{1,128}$`)
	provider         = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	model            = regexp.MustCompile(`^[A-Za-z0-9_./:-]{1,128}$`)
	issuer           = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{1,128}$`)
	jti              = regexp.MustCompile(`^[0-9a-f]{32}$`)
	b64              = base64.RawURLEncoding.Strict()
	strictJSON       = sonic.Config{CaseSensitive: true, DisallowUnknownFields: true, UseNumber: true, ValidateString: true, UseUnicodeErrors: true}.Froze()
	sortedJSON       = sonic.Config{SortMapKeys: true, UseNumber: true, ValidateString: true, UseUnicodeErrors: true}.Froze()
)

type Budgets struct {
	MaxTurns        int64 `json:"max_turns" yaml:"max_turns"`
	MaxToolCalls    int64 `json:"max_tool_calls" yaml:"max_tool_calls"`
	MaxInputTokens  int64 `json:"max_input_tokens" yaml:"max_input_tokens"`
	MaxOutputTokens int64 `json:"max_output_tokens" yaml:"max_output_tokens"`
	WallTimeMS      int64 `json:"wall_time_ms" yaml:"wall_time_ms"`
}

// Envelope contains only the non-secret execution provenance fixed before Run
// admission. Restricted ASCII strings and bounded integers define its hash domain.
type Envelope struct {
	RunID               string  `json:"run_id"`
	ClientRequestID     string  `json:"client_request_id"`
	ActorID             string  `json:"actor_id"`
	Q4DVersion          string  `json:"q4d_version"`
	AgentImageDigest    string  `json:"agent_image_digest"`
	AgentRuntimeVersion string  `json:"agent_runtime_version"`
	BridgeProtocol      int     `json:"bridge_protocol"`
	DSHVersion          string  `json:"dsh_version"`
	AdapterVersion      string  `json:"adapter_version"`
	ProductProfile      string  `json:"product_profile"`
	ProfileRevision     string  `json:"profile_revision"`
	PromptBundleDigest  string  `json:"prompt_bundle_digest"`
	SkillsDigest        string  `json:"skills_digest"`
	ToolCatalogRevision string  `json:"tool_catalog_revision"`
	Provider            string  `json:"provider"`
	Model               string  `json:"model"`
	ModelConfigRevision string  `json:"model_config_revision"`
	Budgets             Budgets `json:"budgets"`
}

type Claims struct {
	Issuer         string   `json:"iss"`
	Audience       []string `json:"aud"`
	IssuedAt       int64    `json:"iat"`
	ExpiresAt      int64    `json:"exp"`
	JTI            string   `json:"jti"`
	SessionID      string   `json:"session_id"`
	RequestHash    string   `json:"request_hash"`
	EnvelopeDigest string   `json:"execution_envelope_digest"`
	Envelope       Envelope `json:"envelope"`
	AllowedTools   []string `json:"allowed_tools"`
}

type protectedHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
	KeyID     string `json:"kid"`
}

// Binding is supplied from an authenticated Bridge request or trusted Go request
// index. Do not populate it by copying unverified claims from the incoming token.
type Binding struct {
	SessionID       string `json:"session_id"`
	RunID           string `json:"run_id"`
	ClientRequestID string `json:"client_request_id"`
	RequestHash     string `json:"request_hash"`
	EnvelopeDigest  string `json:"execution_envelope_digest"`
}

type IssueRequest struct {
	SessionID    string   `json:"session_id"`
	RequestHash  string   `json:"request_hash"`
	Envelope     Envelope `json:"envelope"`
	AllowedTools []string `json:"allowed_tools"`
}

type Signer struct {
	key    ed25519.PrivateKey
	keyID  string
	issuer string
}

func NewSigner(keyID, issuerName string, privateKey ed25519.PrivateKey) (*Signer, error) {
	if !id.MatchString(keyID) || !issuer.MatchString(issuerName) || len(privateKey) != ed25519.PrivateKeySize {
		return nil, ErrConfiguration
	}
	// Reject inconsistent seed/public-key concatenations before using Sign.
	if !bytes.Equal(ed25519.NewKeyFromSeed(privateKey.Seed()), privateKey) {
		return nil, ErrConfiguration
	}
	return &Signer{key: bytes.Clone(privateKey), keyID: keyID, issuer: issuerName}, nil
}

// Issue must be called once for a newly authorized Run. Persist its non-secret
// provenance/deadline before dispatch; a retry must not extend that Run's budget.
func (s *Signer) Issue(input IssueRequest, now time.Time) (string, Claims, error) {
	digest, err := EnvelopeDigest(input.Envelope)
	if err != nil || !id.MatchString(input.SessionID) || !hash.MatchString(input.RequestHash) || input.AllowedTools == nil {
		return "", Claims{}, ErrInvalid
	}
	tools := slices.Clone(input.AllowedTools)
	slices.Sort(tools)
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", Claims{}, ErrConfiguration
	}
	claims := Claims{Issuer: s.issuer, Audience: []string{RuntimeAudience, GatewayAudience}, IssuedAt: now.Unix(),
		ExpiresAt: now.Unix() + (input.Envelope.Budgets.WallTimeMS+999)/1000, JTI: hex.EncodeToString(nonce),
		SessionID: input.SessionID, RequestHash: input.RequestHash, EnvelopeDigest: digest, Envelope: input.Envelope, AllowedTools: tools}
	if !validClaims(claims) || now.UnixMilli() >= claims.DeadlineMillis() {
		return "", Claims{}, ErrInvalid
	}
	token, err := s.SignStored(claims, now)
	return token, claims, err
}

// SignStored re-creates an ephemeral token from the original persisted claims.
// Callers must check current policy first. It never renews the JTI or deadline.
func (s *Signer) SignStored(claims Claims, now time.Time) (string, error) {
	if !validClaims(claims) || claims.Issuer != s.issuer || claims.IssuedAt > now.Unix() {
		return "", ErrInvalid
	}
	if now.UnixMilli() >= claims.DeadlineMillis() {
		return "", ErrExpired
	}
	header, _ := canonical(protectedHeader{Algorithm: "Ed25519", Type: TokenType, KeyID: s.keyID})
	payload, err := canonical(claims)
	if err != nil {
		return "", err
	}
	inputBytes := b64.EncodeToString(header) + "." + b64.EncodeToString(payload)
	token := inputBytes + "." + b64.EncodeToString(ed25519.Sign(s.key, []byte(inputBytes)))
	if len(token) > MaxTokenBytes {
		return "", ErrInvalid
	}
	return token, nil
}

// Authorizer must consult current state (revocation, actor/session ownership,
// model/Profile/Catalog revisions, exact Tool set, original envelope and budgets).
// It is mandatory and is called only after signature/time/binding validation.
// Returning false or an error denies access; errors never enter public error text.
type Authorizer func(context.Context, Claims) (bool, error)

type Verifier struct {
	keys      map[string]ed25519.PublicKey
	issuer    string
	audience  string
	authorize Authorizer
}

func NewVerifier(issuerName, audience string, keys map[string]ed25519.PublicKey, authorize Authorizer) (*Verifier, error) {
	if !issuer.MatchString(issuerName) || (audience != RuntimeAudience && audience != GatewayAudience) || len(keys) == 0 || len(keys) > 16 || authorize == nil {
		return nil, ErrConfiguration
	}
	copyKeys := make(map[string]ed25519.PublicKey, len(keys))
	for keyID, key := range keys {
		if !id.MatchString(keyID) || len(key) != ed25519.PublicKeySize {
			return nil, ErrConfiguration
		}
		copyKeys[keyID] = bytes.Clone(key)
	}
	return &Verifier{keys: copyKeys, issuer: issuerName, audience: audience, authorize: authorize}, nil
}

func (v *Verifier) Verify(ctx context.Context, token string, binding Binding, now time.Time) (Claims, error) {
	if !validBinding(binding) {
		return Claims{}, ErrRejected
	}
	return v.verify(ctx, token, &binding, now)
}

// VerifyCapability verifies the signed identity before invoking current policy.
// Gateways learn the Run binding from this authenticated token, not model fields.
func (v *Verifier) VerifyCapability(ctx context.Context, token string, now time.Time) (Claims, error) {
	return v.verify(ctx, token, nil, now)
}
func (v *Verifier) verify(ctx context.Context, token string, binding *Binding, now time.Time) (Claims, error) {
	if ctx.Err() != nil {
		return Claims{}, ctx.Err()
	}
	if len(token) == 0 || len(token) > MaxTokenBytes {
		return Claims{}, ErrRejected
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, ErrRejected
	}
	headerBytes, err := decode64(parts[0])
	if err != nil || len(headerBytes) > 512 {
		return Claims{}, ErrRejected
	}
	var header protectedHeader
	if decodeCanonical(headerBytes, &header) != nil || header.Algorithm != "Ed25519" || header.Type != TokenType || !id.MatchString(header.KeyID) {
		return Claims{}, ErrRejected
	}
	key, ok := v.keys[header.KeyID]
	if !ok {
		return Claims{}, ErrRejected
	}
	signature, err := decode64(parts[2])
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), signature) {
		return Claims{}, ErrRejected
	}
	payload, err := decode64(parts[1])
	if err != nil {
		return Claims{}, ErrRejected
	}
	var claims Claims
	if decodeCanonical(payload, &claims) != nil || !validClaims(claims) || claims.Issuer != v.issuer || !slices.Contains(claims.Audience, v.audience) || (binding != nil && claims.Binding() != *binding) {
		return Claims{}, ErrRejected
	}
	if claims.IssuedAt > now.Unix() {
		return Claims{}, ErrRejected
	}
	if now.UnixMilli() >= claims.DeadlineMillis() {
		return Claims{}, ErrExpired
	}
	// Clone slices so a policy implementation cannot alter returned authority.
	checked := claims
	checked.Audience = slices.Clone(claims.Audience)
	checked.AllowedTools = slices.Clone(claims.AllowedTools)
	allowed, err := v.authorize(ctx, checked)
	if ctx.Err() != nil {
		return Claims{}, ctx.Err()
	}
	if err != nil || !allowed {
		return Claims{}, ErrRejected
	}
	return claims, nil
}

func (c Claims) Binding() Binding {
	return Binding{SessionID: c.SessionID, RunID: c.Envelope.RunID, ClientRequestID: c.Envelope.ClientRequestID, RequestHash: c.RequestHash, EnvelopeDigest: c.EnvelopeDigest}
}

// ReadStoredClaims validates a non-secret database record, including expired
// records needed for reconciliation. It does not authorize execution.
func ReadStoredClaims(raw string) (Claims, error) {
	var claims Claims
	if len(raw) > MaxTokenBytes || strictJSON.Unmarshal([]byte(raw), &claims) != nil || !validClaims(claims) {
		return Claims{}, ErrInvalid
	}
	return claims, nil
}

// CanonicalClaims is the non-secret control-plane representation, not a token.
func CanonicalClaims(claims Claims) ([]byte, error) {
	if !validClaims(claims) {
		return nil, ErrInvalid
	}
	return canonical(claims)
}
func (c Claims) DeadlineMillis() int64 {
	return min(c.ExpiresAt*1000, c.IssuedAt*1000+c.Envelope.Budgets.WallTimeMS)
}
func validBinding(b Binding) bool {
	return id.MatchString(b.SessionID) && id.MatchString(b.RunID) && id.MatchString(b.ClientRequestID) && hash.MatchString(b.RequestHash) && hash.MatchString(b.EnvelopeDigest)
}

func validEnvelope(e Envelope) bool {
	for _, s := range []string{e.RunID, e.ClientRequestID, e.ActorID, e.ProductProfile, e.ModelConfigRevision} {
		if !id.MatchString(s) {
			return false
		}
	}
	for _, s := range []string{e.AgentImageDigest, e.ProfileRevision, e.PromptBundleDigest, e.SkillsDigest, e.ToolCatalogRevision} {
		if !hash.MatchString(s) {
			return false
		}
	}
	for _, s := range []string{e.Q4DVersion, e.AgentRuntimeVersion, e.DSHVersion, e.AdapterVersion} {
		if !version.MatchString(s) {
			return false
		}
	}
	b := e.Budgets
	return e.BridgeProtocol == 1 && provider.MatchString(e.Provider) && model.MatchString(e.Model) &&
		b.MaxTurns >= 1 && b.MaxTurns <= 4096 && b.MaxToolCalls >= 0 && b.MaxToolCalls <= 4096 && b.MaxInputTokens >= 1 && b.MaxInputTokens <= 100000000 &&
		b.MaxOutputTokens >= 1 && b.MaxOutputTokens <= 10000000 && b.WallTimeMS >= 1 && b.WallTimeMS <= 3600000
}
func validClaims(c Claims) bool {
	if !validEnvelope(c.Envelope) || !issuer.MatchString(c.Issuer) || !jti.MatchString(c.JTI) || !validBinding(c.Binding()) ||
		!slices.Equal(c.Audience, []string{RuntimeAudience, GatewayAudience}) || c.IssuedAt < 1 || c.ExpiresAt > 9007199254740 || c.ExpiresAt <= c.IssuedAt ||
		c.ExpiresAt-c.IssuedAt > (c.Envelope.Budgets.WallTimeMS+999)/1000 || c.AllowedTools == nil || len(c.AllowedTools) > 64 {
		return false
	}
	for i, tool := range c.AllowedTools {
		if !id.MatchString(tool) || (i > 0 && tool <= c.AllowedTools[i-1]) {
			return false
		}
	}
	digest, err := EnvelopeDigest(c.Envelope)
	return err == nil && digest == c.EnvelopeDigest
}

func EnvelopeDigest(e Envelope) (string, error) {
	if !validEnvelope(e) {
		return "", ErrInvalid
	}
	data, err := canonical(e)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Token JSON uses sorted object keys, no whitespace, ASCII strings and integer
// decimal numbers. Comparing the bytes also rejects duplicate keys, omitted
// fields, alternate escapes and numeric spellings in signed tokens. This is a
// restricted protocol profile, not a general-purpose RFC 8785 implementation.
func canonical(value any) ([]byte, error) {
	data, err := strictJSON.Marshal(value)
	if err != nil {
		return nil, ErrInvalid
	}
	var object map[string]any
	if sortedJSON.Unmarshal(data, &object) != nil {
		return nil, ErrInvalid
	}
	data, err = sortedJSON.Marshal(object)
	if err != nil {
		return nil, ErrInvalid
	}
	return data, nil
}
func decodeCanonical(data []byte, dst any) error {
	if !utf8.Valid(data) || !strictJSON.Valid(data) || strictJSON.Unmarshal(data, dst) != nil {
		return ErrRejected
	}
	expected, err := canonical(dst)
	if err != nil || !bytes.Equal(expected, data) {
		return ErrRejected
	}
	return nil
}
func decode64(text string) ([]byte, error) {
	if text == "" {
		return nil, ErrRejected
	}
	data, err := b64.DecodeString(text)
	if err != nil || b64.EncodeToString(data) != text {
		return nil, ErrRejected
	}
	return data, nil
}

func (s *Signer) String() string   { return "agentrunauth.Signer" }
func (s *Signer) GoString() string { return s.String() }

// PromptHash preserves the existing v1 single-text request domain. Text is not
// normalized or trimmed. The explicit key order matches the Bridge producer.
func PromptHash(sessionID, text string) (string, error) {
	if !id.MatchString(sessionID) || !utf8.ValidString(text) || utf8.RuneCountInString(text) < 1 || utf8.RuneCountInString(text) > 32000 {
		return "", ErrInvalid
	}
	value := struct {
		SessionID string `json:"session_id"`
		Content   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}{SessionID: sessionID}
	value.Content = append(value.Content, struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{Type: "text", Text: text})
	data, err := strictJSON.Marshal(value)
	if err != nil {
		return "", ErrInvalid
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
