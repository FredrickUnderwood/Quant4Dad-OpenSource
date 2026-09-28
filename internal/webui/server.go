package webui

import (
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/health"
	appLog "github.com/quant4dad/internal/logger"
	metric "github.com/quant4dad/internal/observability"
	"github.com/quant4dad/internal/utils/httptrust"
)

// New serves the built SPA and proxies same-origin API requests. The upstream
// is resolved on each new connection; its availability does not gate static UI.
type Server struct {
	http.Handler
	root      *os.Root
	transport *http.Transport
}

func (s *Server) Close() error {
	s.transport.CloseIdleConnections()
	return s.root.Close()
}

func New(cfg config.WebConfig, directory string) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, errors.New("web assets unavailable")
	}
	// os.DirFS + ServeFile alone could expose a symlink outside the assets root.
	// Root.FS confines all asset opens, including index.html, to this directory.
	// The root remains open for the HTTP server's lifetime.
	index, err := root.Open("index.html")
	if err != nil {
		root.Close()
		return nil, errors.New("web index unavailable")
	}
	index.Close()
	u, _ := url.Parse(strings.TrimRight(cfg.APIBaseURL, "/"))
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(u)
			r.Out.Host = u.Host
			r.SetXForwarded()
			if proto := r.In.Header.Get("X-Forwarded-Proto"); httptrust.TrustedPeer(r.In.RemoteAddr, cfg.TrustedProxies) && len(r.In.Header.Values("X-Forwarded-Proto")) == 1 && (proto == "http" || proto == "https") {
				r.Out.Header.Set("X-Forwarded-Proto", proto)
			}
			// Preserve the original encoded query, cookies and authorization.
			r.Out.URL.RawQuery = r.In.URL.RawQuery
		},
		Transport:     &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ForceAttemptHTTP2: true, MaxIdleConns: 100, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
		},
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metric.Handler())
	for _, p := range []string{"/healthz", "/livez", "/readyz"} {
		mux.HandleFunc("GET "+p, health.Ready(nil))
	}
	mux.Handle("/api/", proxy)
	files := http.FileServerFS(root.FS())
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		info, err := root.Stat(name)
		if name != "" && err == nil && info.Mode().IsRegular() {
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=2592000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			files.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(name, "assets/") || path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		f, err := root.Open("index.html")
		if err != nil {
			http.Error(w, "web assets unavailable", 503)
			return
		}
		defer f.Close()
		info, err = f.Stat()
		if err != nil {
			http.Error(w, "web assets unavailable", 503)
			return
		}
		http.ServeContent(w, r, "index.html", info.ModTime(), f)
	})
	handler := appLog.TraceMiddleware(metric.Middleware(mux, func(r *http.Request) string {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			return "/api/*"
		}
		return "/*"
	}))
	return &Server{Handler: handler, root: root, transport: proxy.Transport.(*http.Transport)}, nil
}
