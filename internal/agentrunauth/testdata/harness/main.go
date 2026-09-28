// This local authorization fixture is not a production server. Keys and authorization
// records live only in this child process; stdio is its test supervisor channel.
package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/agentrunauth"
)

type command struct {
	ID     int    `json:"id"`
	Method string `json:"method"`
	Params struct {
		SessionID string                `json:"session_id"`
		Text      string                `json:"text"`
		Envelope  agentrunauth.Envelope `json:"envelope"`
		Tools     []string              `json:"allowed_tools"`
		Token     string                `json:"token"`
		Binding   agentrunauth.Binding  `json:"binding"`
		Tool      string                `json:"tool"`
		RunID     string                `json:"run_id"`
	} `json:"params"`
}

func emit(value any) {
	data, err := sonic.MarshalString(value)
	if err != nil {
		os.Exit(1)
	}
	fmt.Println(data)
}
func main() {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		os.Exit(1)
	}
	signer, err := agentrunauth.NewSigner("go-key-v1", "q4d-test-issuer", key)
	if err != nil {
		os.Exit(1)
	}
	runs := map[string]agentrunauth.Claims{}
	revoked := map[string]bool{}
	verifier, err := agentrunauth.NewVerifier("q4d-test-issuer", agentrunauth.GatewayAudience, map[string]ed25519.PublicKey{"go-key-v1": pub}, func(_ context.Context, c agentrunauth.Claims) (bool, error) {
		current, ok := runs[c.Envelope.RunID]
		return ok && !revoked[c.JTI] && current.JTI == c.JTI && current.Binding() == c.Binding() && current.Envelope == c.Envelope && slices.Equal(current.AllowedTools, c.AllowedTools), nil
	})
	if err != nil {
		os.Exit(1)
	}
	emit(map[string]any{"event": "ready", "issuer": "q4d-test-issuer", "keys": map[string]string{"go-key-v1": base64.RawURLEncoding.EncodeToString(pub)}})
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 65536)
	for scanner.Scan() {
		var cmd command
		if sonic.Unmarshal(scanner.Bytes(), &cmd) != nil {
			os.Exit(1)
		}
		var result any
		var callErr error
		switch cmd.Method {
		case "issue":
			var digest, token string
			var claims agentrunauth.Claims
			digest, callErr = agentrunauth.PromptHash(cmd.Params.SessionID, cmd.Params.Text)
			if callErr == nil {
				token, claims, callErr = signer.Issue(agentrunauth.IssueRequest{SessionID: cmd.Params.SessionID, RequestHash: digest, Envelope: cmd.Params.Envelope, AllowedTools: cmd.Params.Tools}, time.Now())
			}
			if callErr == nil {
				runs[claims.Envelope.RunID] = claims
				result = map[string]any{"token": token, "claims": claims, "binding": claims.Binding()}
			}
		case "verify":
			var claims agentrunauth.Claims
			claims, callErr = verifier.Verify(context.Background(), cmd.Params.Token, cmd.Params.Binding, time.Now())
			if callErr == nil && cmd.Params.Tool != "" && !slices.Contains(claims.AllowedTools, cmd.Params.Tool) {
				callErr = agentrunauth.ErrRejected
			}
			result = claims
		case "revoke":
			claims, ok := runs[cmd.Params.RunID]
			if !ok {
				callErr = agentrunauth.ErrRejected
			} else {
				revoked[claims.JTI] = true
			}
			result = struct{}{}
		case "stale_model":
			claims, ok := runs[cmd.Params.RunID]
			if !ok {
				callErr = agentrunauth.ErrRejected
			} else {
				claims.Envelope.ModelConfigRevision = "changed-revision"
				runs[cmd.Params.RunID] = claims
			}
			result = struct{}{}
		default:
			callErr = agentrunauth.ErrInvalid
		}
		if callErr != nil {
			emit(map[string]any{"id": cmd.ID, "error": callErr.Error()})
		} else {
			emit(map[string]any{"id": cmd.ID, "result": result})
		}
	}
	if scanner.Err() != nil {
		os.Exit(1)
	}
}
