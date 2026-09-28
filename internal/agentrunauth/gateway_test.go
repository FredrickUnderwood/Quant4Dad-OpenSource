package agentrunauth

import (
	"context"
	"crypto/ed25519"
	"reflect"
	"testing"
	"time"
)

func TestGatewayCapabilityVerifiesIdentityBeforeCurrentPolicy(t *testing.T) {
	_, _, key, claims, token, now := authFixture(t)
	checks := 0
	allowed := true
	v, err := NewVerifier(claims.Issuer, GatewayAudience, map[string]ed25519.PublicKey{"key-1": key.Public().(ed25519.PublicKey)}, func(_ context.Context, c Claims) (bool, error) {
		checks++
		return allowed && reflect.DeepEqual(c, claims), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.VerifyCapability(context.Background(), token, now)
	if err != nil || !reflect.DeepEqual(got, claims) || checks != 1 {
		t.Fatal("signed identity rejected", err)
	}
	for _, candidate := range []string{"", token + "x", "unsigned.payload.signature"} {
		if _, err = v.VerifyCapability(context.Background(), candidate, now); err == nil {
			t.Fatal("forgery accepted")
		}
	}
	if checks != 1 {
		t.Fatal("unverified claims reached current policy")
	}
	if _, err = v.VerifyCapability(context.Background(), token, time.UnixMilli(claims.DeadlineMillis())); err != ErrExpired {
		t.Fatal("expired capability accepted", err)
	}
	allowed = false
	if _, err = v.VerifyCapability(context.Background(), token, now); err != ErrRejected {
		t.Fatal("revoked capability accepted", err)
	}
}
