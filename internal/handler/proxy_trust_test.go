package handler

import (
	"github.com/quant4dad/config"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoginForwardedHTTPSRequiresTrustedPeer(t *testing.T) {
	for _, tc := range []struct {
		name, peer, proto string
		trusted           []string
		secure            bool
	}{
		{name: "untrusted", peer: "198.51.100.7:1234", proto: "https"},
		{name: "trusted", peer: "127.0.0.1:1234", proto: "https", trusted: []string{"127.0.0.1"}, secure: true},
		{name: "wrong-peer", peer: "198.51.100.7:1234", proto: "https", trusted: []string{"127.0.0.1"}},
		{name: "ambiguous", peer: "127.0.0.1:1234", proto: "https,http", trusted: []string{"127.0.0.1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Security: config.SecurityConfig{Token: "fixture"}, Server: config.ServerConfig{TrustedProxies: tc.trusted}}
			server := NewServer(cfg, Handlers{})
			request := httptest.NewRequest("POST", "/api/v1/login", strings.NewReader(`{"token":"fixture"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Forwarded-Proto", tc.proto)
			request.RemoteAddr = tc.peer
			response := httptest.NewRecorder()
			server.engine.ServeHTTP(response, request)
			cookies := response.Result().Cookies()
			if response.Code != 200 || len(cookies) != 1 || cookies[0].Secure != tc.secure {
				t.Fatalf("unexpected cookie transport protection: %d", response.Code)
			}
		})
	}
}
