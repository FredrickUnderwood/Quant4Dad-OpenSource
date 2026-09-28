package handler

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"runtime/debug"
	"time"

	ginzap "github.com/gin-contrib/zap"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/health"
	"github.com/quant4dad/internal/logger"
	metric "github.com/quant4dad/internal/observability"
	"github.com/quant4dad/internal/utils/httptrust"
)

type Handlers struct {
	Readiness       health.Check
	Strategy        *StrategyHandler
	Indicator       *IndicatorHandler
	Instrument      *InstrumentHandler
	Cost            *CostHandler
	Backtest        *BacktestHandler
	DataSync        *DataSyncHandler
	Pipeline        *PipelineHandler
	Setting         *SettingHandler
	News            *NewsHandler
	Archive         *ArchiveHandler
	AgentBootstrap  *AgentBootstrapHandler
	AgentModelProbe *AgentModelProbeHandler
	AgentSession    *AgentSessionHandler
	AgentRun        *AgentRunHandler
	AgentGateway    *AgentToolGatewayHandler
	AgentApproval   *AgentApprovalHandler
	AgentToolResult *AgentToolResultHandler
	ExternalMCP     http.Handler
}

type Server struct {
	cfg    *config.Config
	engine *gin.Engine
	http   *http.Server
}

// authCookieName is the cookie the browser keeps the login in. HttpOnly, so XSS cannot steal
// it.
const authCookieName = "q4d_auth"

// authCookieMaxAge is how long a login lasts: 7 days. On expiry the UI gets a 401 and
// returns to /login.
const authCookieMaxAge = 7 * 24 * 3600

func NewServer(cfg *config.Config, h Handlers) *Server {
	gin.SetMode(gin.ReleaseMode)
	e := gin.New()

	// Deploying behind a reverse proxy requires configuring the trusted proxies explicitly;
	// empty trusts none. Gin's default of trusting 0.0.0.0/0 is not safe.
	_ = e.SetTrustedProxies(cfg.Server.TrustedProxies)

	// The logger propagates trace context. Control routes use a bounded, sanitized
	// access logger; all other routes retain ginzap.
	e.Use(logger.GinMiddleware())
	e.Use(func(c *gin.Context) {
		c.Set("q4d_https", httptrust.HTTPS(c.Request, cfg.Server.TrustedProxies))
		c.Next()
	})
	e.Use(agentControlAccessLog())
	e.Use(ginzap.GinzapWithConfig(logger.L(), &ginzap.Config{
		TimeFormat: time.RFC3339,
		UTC:        true,
		Skipper:    agentControlRequest,
		Context: func(c *gin.Context) []zap.Field {
			return []zap.Field{zap.String("trace_id", logger.TraceIDFromContext(c.Request.Context()))}
		},
	}))
	// Recovery runs inside metrics so recovered panics count as HTTP 500.
	e.Use(metric.GinMiddleware())
	e.Use(recoveryMiddleware())

	// /healthz is always public, for the docker healthcheck and load balancer probes.
	e.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	e.GET("/livez", gin.WrapF(health.Ready(nil)))
	e.GET("/readyz", gin.WrapF(health.Ready(h.Readiness)))
	e.GET("/metrics", gin.WrapH(metric.Handler()))
	if cfg.Agent.Enabled && cfg.Agent.Bootstrap.Enabled && h.AgentBootstrap != nil {
		h.AgentBootstrap.Register(e)
	}
	if cfg.Agent.Enabled && cfg.Agent.Runs.Enabled && cfg.Agent.Bootstrap.Enabled && h.AgentRun != nil {
		h.AgentRun.RegisterControl(e)
	}
	if cfg.Agent.Gateway.Enabled && cfg.ValidateAgentGateway() == nil && h.AgentGateway != nil {
		h.AgentGateway.Register(e)
	}
	if cfg.MCP.AgentToolsEnabled && cfg.ValidateExternalMCP() == nil && h.ExternalMCP != nil {
		e.Any("/external/mcp", gin.WrapH(h.ExternalMCP))
	}

	token := cfg.Security.Token
	if token != "" {
		logger.L().Info("token auth enabled")
	}

	v1 := e.Group("/api/v1")

	// The three login endpoints must be registered before the token middleware, or a
	// logged-out user could never log in.
	v1.POST("/login", loginHandler(token))
	v1.POST("/logout", logoutHandler())
	v1.GET("/auth/status", authStatusHandler(token))

	if token != "" {
		v1.Use(tokenAuthMiddleware(token))
	}

	if h.Strategy != nil {
		h.Strategy.Register(v1)
	}
	if h.Indicator != nil {
		h.Indicator.Register(v1)
	}
	if h.Instrument != nil {
		h.Instrument.Register(v1)
	}
	if h.Cost != nil {
		h.Cost.Register(v1)
	}
	if h.Backtest != nil {
		h.Backtest.Register(v1)
	}
	if h.DataSync != nil {
		h.DataSync.Register(v1)
	}
	if h.Pipeline != nil {
		h.Pipeline.Register(v1)
	}
	if h.Setting != nil {
		h.Setting.Register(v1)
		if cfg.Agent.Enabled {
			h.Setting.RegisterAgentModels(v1)
		}
	}
	if cfg.Agent.Enabled && cfg.Agent.ModelProbe.Enabled && h.AgentModelProbe != nil {
		h.AgentModelProbe.Register(v1)
	}
	if cfg.Agent.Enabled && cfg.Agent.Sessions.Enabled && token != "" && h.AgentSession != nil {
		h.AgentSession.Register(v1)
	}
	if cfg.Agent.Enabled && cfg.Agent.Runs.Enabled && cfg.Agent.Sessions.Enabled && token != "" && h.AgentRun != nil {
		h.AgentRun.Register(v1)
		if h.AgentApproval != nil {
			h.AgentApproval.Register(v1)
		}
		if h.AgentToolResult != nil {
			h.AgentToolResult.Register(v1)
		}
	}
	if h.News != nil {
		h.News.Register(v1)
	}
	if h.Archive != nil {
		h.Archive.Register(v1)
	}

	return &Server{cfg: cfg, engine: e}
}

// Recovery must not dump the request (including auth cookies / headers) or the
// panic value, which may contain business data. The stack and trace locate it.
func recoveryMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if value := recover(); value != nil {
				logger.Error(c.Request.Context(), "http handler panic",
					zap.String("panic_type", fmt.Sprintf("%T", value)),
					zap.ByteString("stack", debug.Stack()))
				c.AbortWithStatus(http.StatusInternalServerError)
			}
		}()
		c.Next()
	}
}

// tokenAuthMiddleware checks the token in the request cookie against the configured one,
// using ConstantTimeCompare to close the timing side channel.
func tokenAuthMiddleware(expected string) gin.HandlerFunc {
	expectedBytes := []byte(expected)
	return func(c *gin.Context) {
		got, err := c.Cookie(authCookieName)
		if err != nil || subtle.ConstantTimeCompare([]byte(got), expectedBytes) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, ErrorResponse{Code: 401, Message: "unauthorized"})
			return
		}
		c.Next()
	}
}

type loginRequest struct {
	Token string `json:"token"`
}

func loginHandler(expected string) gin.HandlerFunc {
	expectedBytes := []byte(expected)
	return func(c *gin.Context) {
		// With no token configured, succeed outright, so local development is not constantly
		// bounced to the login page.
		if expected == "" {
			c.JSON(http.StatusOK, gin.H{"ok": true, "auth_required": false})
			return
		}
		var req loginRequest
		if err := c.ShouldBindJSON(&req); err != nil || req.Token == "" {
			c.JSON(http.StatusBadRequest, ErrorResponse{Code: 400, Message: "token required"})
			return
		}
		if subtle.ConstantTimeCompare([]byte(req.Token), expectedBytes) != 1 {
			c.JSON(http.StatusUnauthorized, ErrorResponse{Code: 401, Message: "invalid token"})
			return
		}
		setAuthCookie(c, req.Token)
		c.JSON(http.StatusOK, gin.H{"ok": true, "auth_required": true})
	}
}

func logoutHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		clearAuthCookie(c)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

// authStatusHandler lets the UI tell whether it is logged in, and so whether to show the
// login page.
func authStatusHandler(expected string) gin.HandlerFunc {
	expectedBytes := []byte(expected)
	return func(c *gin.Context) {
		if expected == "" {
			c.JSON(http.StatusOK, gin.H{"authenticated": true, "auth_required": false})
			return
		}
		got, err := c.Cookie(authCookieName)
		authed := err == nil && subtle.ConstantTimeCompare([]byte(got), expectedBytes) == 1
		c.JSON(http.StatusOK, gin.H{"authenticated": authed, "auth_required": true})
	}
}

// Only TLS itself or an explicitly trusted immediate proxy controls Secure cookies.
func isTLSRequest(c *gin.Context) bool {
	return c.Request.TLS != nil || c.GetBool("q4d_https")
}

func setAuthCookie(c *gin.Context, value string) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(authCookieName, value, authCookieMaxAge, "/", "", isTLSRequest(c), true)
}

func clearAuthCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(authCookieName, "", -1, "/", "", isTLSRequest(c), true)
}

func (s *Server) Start() error {
	s.http = &http.Server{Addr: s.cfg.Server.Addr, Handler: s.engine}
	return s.http.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.http == nil {
		return nil
	}
	return s.http.Shutdown(ctx)
}
