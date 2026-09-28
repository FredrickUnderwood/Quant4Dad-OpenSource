package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFeishuHTTPAndBusinessStatusMustBothSucceed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		ok     bool
	}{
		{"success", 200, `{"code":0}`, true},
		{"legacy success", 200, `{"StatusCode":0}`, true},
		{"missing acknowledgement", 200, `{}`, false},
		{"conflicting acknowledgement", 200, `{"code":0,"StatusCode":123}`, false},
		{"HTTP failure with success body", 503, `{"code":0}`, false},
		{"business failure", 200, `{"code":123,"msg":"denied"}`, false},
		{"legacy business failure", 200, `{"StatusCode":123}`, false},
		{"invalid response", 200, `invalid`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			err := newFeishuSender(FeishuConfig{WebhookURL: server.URL}).Send(context.Background(), Message{Title: "Agent测试", Body: "local only"})
			if (err == nil) != tc.ok {
				t.Fatalf("success=%v error=%v", tc.ok, err)
			}
		})
	}
}
