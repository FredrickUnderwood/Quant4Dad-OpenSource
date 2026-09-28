package agentrunauth

import (
	"crypto/ed25519"
	"encoding/base64"
	"regexp"
)

var approvalPart = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ApprovalReceipt uses domain separation from JWT capability signing. Neither
// the browser nor the model receives this signature; the BFF sends it directly
// to the owned Runtime approval waiter. Repeating a decision yields the same
// receipt without renewing its stored expiration or nonce.
func (s *Signer) ApprovalReceipt(id, nonce string) (string, error) {
	if s == nil || !approvalPart.MatchString(id) || !approvalPart.MatchString(nonce) {
		return "", ErrConfiguration
	}
	signature := ed25519.Sign(s.key, []byte("q4d.approval.v1:"+id+":"+nonce))
	return base64.RawURLEncoding.EncodeToString(signature), nil
}
