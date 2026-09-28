package webui

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quant4dad/config"
)

func TestWebSharedConfigProxyAndSPA(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "q4d_auth=fixture; Path=/; HttpOnly; Secure; SameSite=Lax")
		w.Header().Set("X-Request-ID", r.Header.Get("X-Request-ID"))
		fmt.Fprintf(w, "%s|%s|%s|%s|%s|%s|%s", r.RequestURI, r.Host, r.Header.Get("X-Forwarded-Proto"), r.Header.Get("X-Request-ID"), r.Method, r.Header.Get("Authorization"), r.Header.Get("Cookie"))
	}))
	defer upstream.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<div id=\"root\">fixture</div>"), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := New(config.WebConfig{APIBaseURL: upstream.URL + "/svc-quant4dad-api/", TrustedProxies: []string{"127.0.0.1"}}, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	web := httptest.NewServer(handler)
	defer web.Close()
	for _, path := range []string{"/", "/strategies/fixture"} {
		resp, err := http.Get(web.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(body), "fixture</div>") {
			t.Fatal("SPA route failed")
		}
	}
	request, _ := http.NewRequest("POST", web.URL+"/api/v1/probe?value=a%2Fb&n=2", strings.NewReader("{}"))
	request.Host = "quant.example.com"
	for key, value := range map[string]string{"X-Forwarded-Proto": "https", "X-Request-ID": "trace-web-test", "Authorization": "Bearer fixture-token", "Cookie": "q4d_auth=client"} {
		request.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	want := "/svc-quant4dad-api/api/v1/probe?value=a%2Fb&n=2|" + strings.TrimPrefix(upstream.URL, "http://") + "|https|trace-web-test|POST|Bearer fixture-token|q4d_auth=client"
	if string(body) != want || !strings.Contains(resp.Header.Get("Set-Cookie"), "HttpOnly; Secure") || resp.Header.Get("X-Request-ID") != "trace-web-test" {
		t.Fatalf("proxy contract failed: %s", body)
	}
	upstream.Close()
	for _, check := range []struct {
		path string
		code int
	}{{"/readyz", 200}, {"/healthz", 200}, {"/api/v1/probe", 502}, {"/assets/missing.js", 404}} {
		resp, err := http.Get(web.URL + check.path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != check.code {
			t.Fatalf("%s: %d", check.path, resp.StatusCode)
		}
	}
}

func TestWebRejectsEscapingAssetsAndInvalidUpstream(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("index"), 0600); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "leak.txt")); err != nil {
		t.Fatal(err)
	}
	h, err := New(config.WebConfig{APIBaseURL: "http://127.0.0.1:1"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/leak.txt", nil))
	if w.Code != 404 || strings.Contains(w.Body.String(), "private") {
		t.Fatal("symlink escaped asset root")
	}
	for _, upstream := range []string{"http://user:secret@api", "http://api?secret=value", "http://api\n/", "file:///etc/passwd"} {
		if _, err := New(config.WebConfig{APIBaseURL: upstream}, dir); err == nil {
			t.Fatal("invalid upstream accepted")
		}
	}
}
