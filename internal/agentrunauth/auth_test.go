package agentrunauth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixtureEnvelope(t *testing.T) Envelope {
	t.Helper()
	data, err := os.ReadFile("../../agent-runtime/evals/fixture-v1/contracts/authorization/fixtures/valid.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Envelope Envelope `json:"envelope"`
	}
	if sortedJSON.Unmarshal(data, &fixture) != nil {
		t.Fatal("invalid shared fixture")
	}
	return fixture.Envelope
}
func authFixture(t *testing.T) (*Signer, *Verifier, ed25519.PrivateKey, Claims, string, time.Time) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewSigner("key-1", "q4d-test-issuer", key)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier("q4d-test-issuer", GatewayAudience, map[string]ed25519.PublicKey{"key-1": pub}, func(context.Context, Claims) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1788900000, 0)
	requestHash, _ := PromptHash("session-1", "query")
	token, claims, err := signer.Issue(IssueRequest{SessionID: "session-1", RequestHash: requestHash, Envelope: fixtureEnvelope(t), AllowedTools: []string{"query_kline"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	return signer, verifier, key, claims, token, now
}
func resign(key ed25519.PrivateKey, header, payload []byte) string {
	input := b64.EncodeToString(header) + "." + b64.EncodeToString(payload)
	return input + "." + b64.EncodeToString(ed25519.Sign(key, []byte(input)))
}

func TestSharedHashVectors(t *testing.T) {
	data, err := os.ReadFile("../../agent-runtime/evals/fixture-v1/contracts/authorization/fixtures/valid.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Envelope Envelope `json:"envelope"`
		Digest   string   `json:"execution_envelope_digest"`
		Prompts  []struct {
			Text string `json:"text"`
			Hash string `json:"request_hash"`
		} `json:"prompt_cases"`
	}
	if strictJSON.Unmarshal(data, &f) != nil {
		t.Fatal("invalid shared fixtures")
	}
	digest, err := EnvelopeDigest(f.Envelope)
	if err != nil || digest != f.Digest {
		t.Fatal("envelope hash differs across languages")
	}
	for i, p := range f.Prompts {
		got, err := PromptHash("session-1", p.Text)
		if err != nil || got != p.Hash {
			t.Fatalf("prompt hash differs for vector %d", i)
		}
	}
}

func TestSignVerifyAndBinding(t *testing.T) {
	_, verifier, _, claims, token, now := authFixture(t)
	got, err := verifier.Verify(context.Background(), token, claims.Binding(), now)
	if err != nil || !reflect.DeepEqual(got, claims) {
		t.Fatalf("valid token rejected: %v", err)
	}
	for _, edit := range []func(*Binding){func(b *Binding) { b.SessionID = "other" }, func(b *Binding) { b.RunID = "other" }, func(b *Binding) { b.ClientRequestID = "other" }, func(b *Binding) { b.RequestHash = "sha256:" + strings.Repeat("b", 64) }, func(b *Binding) { b.EnvelopeDigest = "sha256:" + strings.Repeat("b", 64) }} {
		binding := claims.Binding()
		edit(&binding)
		if _, err := verifier.Verify(context.Background(), token, binding, now); err != ErrRejected {
			t.Fatal("unbound token accepted")
		}
	}
}

func TestLongRunBudgetAdmissionAndDeadline(t *testing.T) {
	signer, verifier, _, original, _, now := authFixture(t)
	data, err := os.ReadFile("../../agent-runtime/fixtures/long-run-budgets.json")
	if err != nil {
		t.Fatal(err)
	}
	envelope := original.Envelope
	if err := strictJSON.Unmarshal(data, &envelope.Budgets); err != nil {
		t.Fatal(err)
	}
	token, claims, err := signer.Issue(IssueRequest{SessionID: original.SessionID, RequestHash: original.RequestHash, Envelope: envelope, AllowedTools: original.AllowedTools}, now)
	if err != nil {
		t.Fatal(err)
	}
	deadline := now.Add(30 * time.Minute)
	if claims.DeadlineMillis() != deadline.UnixMilli() {
		t.Fatal("long run deadline changed")
	}
	for _, elapsed := range []time.Duration{10 * time.Minute, 29*time.Minute + 59*time.Second} {
		if _, err := verifier.Verify(context.Background(), token, claims.Binding(), now.Add(elapsed)); err != nil {
			t.Fatalf("long run stopped prematurely: %v", err)
		}
	}
	if _, err := verifier.Verify(context.Background(), token, claims.Binding(), deadline); !errors.Is(err, ErrExpired) {
		t.Fatal("long run outlived its budget", err)
	}
}

func TestSignStoredNeverRenewsAuthority(t *testing.T) {
	signer, _, _, claims, original, now := authFixture(t)
	token, err := signer.SignStored(claims, now.Add(time.Second))
	if err != nil || token != original {
		t.Fatal("retry changed original token", err)
	}
	deadline := time.UnixMilli(claims.DeadlineMillis())
	if _, err = signer.SignStored(claims, deadline); !errors.Is(err, ErrExpired) {
		t.Fatal("expired claims renewed", err)
	}
	data, err := CanonicalClaims(claims)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := ReadStoredClaims(string(data))
	if err != nil || !reflect.DeepEqual(stored, claims) {
		t.Fatal("expired provenance cannot be reconciled", err)
	}
	claims.Envelope.Model = "forged"
	if _, err = signer.SignStored(claims, now); !errors.Is(err, ErrInvalid) {
		t.Fatal("inconsistent envelope accepted", err)
	}
}

func TestKeyAndAlgorithmIsolation(t *testing.T) {
	_, v, key, c, token, now := authFixture(t)
	parts := strings.Split(token, ".")
	payload, _ := decode64(parts[1])
	for _, header := range []string{`{"alg":"none","kid":"key-1","typ":"q4d-run-capability-v1+jwt"}`, `{"alg":"EdDSA","kid":"key-1","typ":"q4d-run-capability-v1+jwt"}`, `{"alg":"HS256","kid":"key-1","typ":"q4d-run-capability-v1+jwt"}`, `{"alg":"Ed25519","kid":"unknown","typ":"q4d-run-capability-v1+jwt"}`, `{"alg":"Ed25519","kid":"key-1","typ":"JWT"}`, `{"alg":"Ed25519","jku":"https://untrusted.invalid/keys","kid":"key-1","typ":"q4d-run-capability-v1+jwt"}`} {
		if _, err := v.Verify(context.Background(), resign(key, []byte(header), payload), c.Binding(), now); err != ErrRejected {
			t.Fatal("unapproved header accepted")
		}
	}
	_, wrongKey, _ := ed25519.GenerateKey(rand.Reader)
	header, _ := decode64(parts[0])
	if _, err := v.Verify(context.Background(), resign(wrongKey, header, payload), c.Binding(), now); err != ErrRejected {
		t.Fatal("wrong key accepted")
	}
	other, _ := NewVerifier("other-issuer", RuntimeAudience, v.keys, func(context.Context, Claims) (bool, error) { return true, nil })
	if _, err := other.Verify(context.Background(), token, c.Binding(), now); err != ErrRejected {
		t.Fatal("wrong issuer accepted")
	}
}

func TestCanonicalClaimsRejectAmbiguousEncodings(t *testing.T) {
	_, v, key, c, token, now := authFixture(t)
	parts := strings.Split(token, ".")
	header, _ := decode64(parts[0])
	payload, _ := decode64(parts[1])
	for name, body := range map[string]string{
		"duplicate":     strings.Replace(string(payload), `"iat":1788900000`, `"iat":1,"iat":1788900000`, 1),
		"missing zero":  strings.Replace(string(payload), `"max_tool_calls":20,`, "", 1),
		"missing field": strings.Replace(string(payload), `"bridge_protocol":1,`, "", 1),
		"null":          strings.Replace(string(payload), `"max_turns":12`, `"max_turns":null`, 1),
		"wrong case":    strings.Replace(string(payload), `"run_id"`, `"Run_id"`, 1),
		"decimal":       strings.Replace(string(payload), `"max_turns":12`, `"max_turns":12.0`, 1),
		"whitespace":    " " + string(payload),
		"bom":           "\ufeff" + string(payload),
		"unknown":       strings.TrimSuffix(string(payload), "}") + `,"secret":"sensitive"}`,
		"escape":        strings.Replace(string(payload), `"run-1"`, `"\u0072un-1"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(context.Background(), resign(key, header, []byte(body)), c.Binding(), now); err != ErrRejected {
				t.Fatal("ambiguous signed claims accepted")
			}
		})
	}
	for _, bad := range []string{token + "=", token + ".extra", parts[0] + "." + parts[1] + ".AA", strings.Repeat("a", 8193), token + "\n", parts[0] + "=." + parts[1] + "." + parts[2]} {
		if _, err := v.Verify(context.Background(), bad, c.Binding(), now); err != ErrRejected {
			t.Fatal("malformed token accepted")
		}
	}
}

func TestSignedPolicyAndDigestValidation(t *testing.T) {
	_, v, key, c, token, now := authFixture(t)
	header, _ := decode64(strings.Split(token, ".")[0])
	for _, edit := range []func(*Claims){func(c *Claims) { c.Audience = []string{GatewayAudience} }, func(c *Claims) { c.Envelope.Model = "other" }, func(c *Claims) { c.AllowedTools = []string{"b", "a"} }, func(c *Claims) { c.AllowedTools = []string{"a", "a"} }, func(c *Claims) { c.Envelope.Budgets.MaxTurns = 4097 }, func(c *Claims) { c.ExpiresAt += 1 }} {
		bad := c
		edit(&bad)
		payload, _ := canonical(bad)
		if _, err := v.Verify(context.Background(), resign(key, header, payload), c.Binding(), now); err != ErrRejected {
			t.Fatal("invalid signed authority accepted")
		}
	}
}

func TestExpirationAndFutureIssueTime(t *testing.T) {
	_, v, _, c, token, now := authFixture(t)
	if _, err := v.Verify(context.Background(), token, c.Binding(), now.Add(-time.Second)); err != ErrRejected {
		t.Fatal("future-issued token accepted")
	}
	if _, err := v.Verify(context.Background(), token, c.Binding(), time.UnixMilli(c.DeadlineMillis()-1)); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), token, c.Binding(), time.UnixMilli(c.DeadlineMillis())); err != ErrExpired {
		t.Fatal("expiry boundary was extended")
	}
}

func TestLiveAuthorizerRequiredAndRechecked(t *testing.T) {
	_, original, _, claims, token, now := authFixture(t)
	if _, err := NewVerifier("q4d-test-issuer", GatewayAudience, original.keys, nil); err != ErrConfiguration {
		t.Fatal("missing policy accepted")
	}
	active := true
	calls := 0
	v, _ := NewVerifier("q4d-test-issuer", GatewayAudience, original.keys, func(_ context.Context, c Claims) (bool, error) {
		calls++
		c.AllowedTools[0] = "mutated"
		return active, nil
	})
	got, err := v.Verify(context.Background(), token, claims.Binding(), now)
	if err != nil || got.AllowedTools[0] != "query_kline" {
		t.Fatal("policy mutated returned authority")
	}
	active = false
	if _, err := v.Verify(context.Background(), token, claims.Binding(), now); err != ErrRejected || calls != 2 {
		t.Fatal("revocation not rechecked")
	}
	if _, err := v.Verify(context.Background(), "invalid", claims.Binding(), now); err != ErrRejected || calls != 2 {
		t.Fatal("unverified claims reached policy")
	}
	v, _ = NewVerifier("q4d-test-issuer", GatewayAudience, original.keys, func(context.Context, Claims) (bool, error) { return false, errors.New("sensitive-database-detail") })
	if _, err := v.Verify(context.Background(), token, claims.Binding(), now); err != ErrRejected {
		t.Fatal("policy error was exposed")
	}
}

func TestKeysAndIssuerAreImmutable(t *testing.T) {
	signer, verifier, key, claims, token, now := authFixture(t)
	for i := range key {
		key[i] = 0
	}
	if strings.Contains(fmt.Sprintf("%v %+v %#v", signer, signer, signer), "key-") {
		t.Fatal("signer formatting exposed internals")
	}
	if _, err := verifier.Verify(context.Background(), token, claims.Binding(), now); err != nil {
		t.Fatal(err)
	}
	input := IssueRequest{SessionID: claims.SessionID, RequestHash: claims.RequestHash, Envelope: claims.Envelope, AllowedTools: []string{}}
	emptyToken, empty, err := signer.Issue(input, now)
	if err != nil {
		t.Fatal("valid empty tool profile rejected")
	}
	if _, err := verifier.Verify(context.Background(), emptyToken, empty.Binding(), now); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSigner("key-1", "issuer", bytes.Repeat([]byte{1}, 64)); err != ErrConfiguration {
		t.Fatal("inconsistent private key accepted")
	}
}

func TestInvalidIssuanceAndCancelledVerification(t *testing.T) {
	signer, v, _, claims, token, now := authFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := v.Verify(ctx, token, claims.Binding(), now); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled verification continued")
	}
	for _, tools := range [][]string{nil, {"query_kline", "query_kline"}, {"bad tool"}} {
		if _, _, err := signer.Issue(IssueRequest{SessionID: claims.SessionID, RequestHash: claims.RequestHash, Envelope: claims.Envelope, AllowedTools: tools}, now); err != ErrInvalid {
			t.Fatal("invalid issue accepted")
		}
	}
	if _, err := PromptHash("session-1", "\xff"); err != ErrInvalid {
		t.Fatal("invalid UTF-8 prompt accepted")
	}
	if _, err := PromptHash("session-1", strings.Repeat("a", 32001)); err != ErrInvalid {
		t.Fatal("oversized prompt accepted")
	}
	for _, ending := range []string{"\n", "\r", "\u2028", "\u2029"} {
		if _, err := PromptHash("session-1"+ending, "query"); err != ErrInvalid {
			t.Fatal("line terminator in session identity accepted")
		}
		envelope := claims.Envelope
		envelope.Model += ending
		if _, err := EnvelopeDigest(envelope); err != ErrInvalid {
			t.Fatal("line terminator in envelope identity accepted")
		}
	}
}
