package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/service"
)

type DeploymentSettingItem struct {
	Key        string `json:"key"`
	Label      string `json:"label"`
	Value      string `json:"value"`
	ChangeHint string `json:"change_hint"`
}
type DeploymentSettingsView struct {
	StorageBackend        string                  `json:"storage_backend"`
	Timezone              string                  `json:"timezone"`
	AgentEnabled          bool                    `json:"agent_enabled"`
	MCPEnabled            bool                    `json:"mcp_enabled"`
	AuthenticationEnabled bool                    `json:"authentication_enabled"`
	BackgroundEnabled     bool                    `json:"background_enabled"`
	BacktestWorkers       int                     `json:"backtest_workers"`
	Items                 []DeploymentSettingItem `json:"items"`
}
type IntegrationSettingHandler struct {
	app        *application.IntegrationSettingsApplication
	deployment DeploymentSettingsView
}

func NewIntegrationSettingHandler(app *application.IntegrationSettingsApplication, cfg *config.Config, background bool) *IntegrationSettingHandler {
	view := DeploymentSettingsView{StorageBackend: cfg.Storage.Backend, Timezone: time.Now().Location().String(), AgentEnabled: cfg.Agent.Enabled, MCPEnabled: cfg.MCP.AgentToolsEnabled, AuthenticationEnabled: cfg.Security.Token != "", BackgroundEnabled: background, BacktestWorkers: cfg.Backtest.WorkerPool}
	state := func(enabled bool) string {
		if enabled {
			return "已启用"
		}
		return "未启用"
	}
	view.Items = []DeploymentSettingItem{
		{Key: "storage", Label: "数据库", Value: cfg.Storage.Backend, ChangeHint: "安装时选择 SQLite 或 --mysql-dsn-file；变更前备份数据并执行数据迁移。凭据仅保存在私有配置。"},
		{Key: "timezone", Label: "调度时区", Value: view.Timezone, ChangeHint: "通过部署配置 Q4D_TIMEZONE 修改并重新创建容器，采集和归档时刻均使用此时区。"},
		{Key: "agent", Label: "Agent Runtime 扩展", Value: state(cfg.Agent.Enabled), ChangeHint: "运行 ./scripts/install.sh --with-agent 启用；模型凭据在上方模型设置中填写。"},
		{Key: "mcp", Label: "MCP 扩展", Value: state(cfg.MCP.AgentToolsEnabled), ChangeHint: "运行 ./scripts/install.sh --with-mcp 启用；客户端使用 <state-dir>/config/mcp-token，授权包括全部业务写工具。"},
		{Key: "login", Label: "网页登录认证", Value: state(cfg.Security.Token != ""), ChangeHint: "登录凭据位于 <state-dir>/config/login-token；轮换需同步私有配置并重新创建服务，页面不显示凭据。"},
		{Key: "network", Label: "端口与访问地址", Value: "部署配置管理", ChangeHint: "安装器 --web-port / --mcp-port / --bind；公网使用 HTTPS 代理并显式配置可信代理。"},
		{Key: "background", Label: "后台任务", Value: state(background), ChangeHint: "--no-background 会阻止自动采集、自动归档和后台 worker；在设置页保存启用项不会越过此部署开关。"},
		{Key: "backtest_workers", Label: "回测工作线程", Value: strconv.Itoa(cfg.Backtest.WorkerPool), ChangeHint: "backtest.worker_pool 为启动参数，修改私有 API YAML 后重启。默认费用模型可在回测设置中即时修改。"},
		{Key: "settings_storage", Label: "数据源与 OSS 凭据存储", Value: "私有配置文件", ChangeHint: "设置保存在 API 数据目录下的 settings/integrations.yaml（文件 0600、目录 0700）；备份持久化数据时一并保留。"},
	}
	return &IntegrationSettingHandler{app: app, deployment: view}
}
func (h *IntegrationSettingHandler) Register(rg *gin.RouterGroup) {
	g := rg.Group("/settings")
	g.GET("/integrations", h.get)
	g.PUT("/integrations", h.save)
	g.POST("/integrations/test-market", func(c *gin.Context) { h.probe(c, "market") })
	g.POST("/integrations/test-oss", func(c *gin.Context) { h.probe(c, "oss") })
	g.GET("/deployment", func(c *gin.Context) { modelHeaders(c); ok(c, h.deployment) })
}
func (h *IntegrationSettingHandler) get(c *gin.Context) { modelHeaders(c); ok(c, h.app.View()) }
func (h *IntegrationSettingHandler) save(c *gin.Context) {
	modelHeaders(c)
	var patch service.IntegrationPatch
	if !decodeModelBody(c, &patch) {
		return
	}
	view, err := h.app.Save(c.Request.Context(), patch)
	if err != nil {
		integrationHTTPError(c, err)
		return
	}
	ok(c, view)
}
func (h *IntegrationSettingHandler) probe(c *gin.Context, target string) {
	modelHeaders(c)
	var request struct {
		ExpectedRevision *uint64 `json:"expected_revision"`
	}
	if !decodeModelBody(c, &request) {
		return
	}
	if request.ExpectedRevision == nil {
		integrationHTTPError(c, service.ErrIntegrationInput)
		return
	}
	if err := h.app.Probe(c.Request.Context(), *request.ExpectedRevision, target); err != nil {
		integrationHTTPError(c, err)
		return
	}
	message := "目录读取连接检查通过；其他行情与 ETF 接口权限以账号授权为准"
	if target == "oss" {
		message = "OSS Bucket 信息读取通过；此检查不会上传或删除对象，也不证明归档上传权限"
	}
	ok(c, gin.H{"ok": true, "message": message})
}
func integrationHTTPError(c *gin.Context, err error) {
	status, message := http.StatusBadRequest, err.Error()
	switch {
	case errors.Is(err, domain.ErrResourceConflict):
		status, message = http.StatusConflict, "设置已被其他操作修改，请重新加载后保存"
	case errors.Is(err, application.ErrArchiveBusy):
		status, message = http.StatusConflict, "归档任务正在运行，请结束后修改 OSS 配置"
	case errors.Is(err, service.ErrIntegrationStore):
		status, message = http.StatusInternalServerError, "私有设置文件不可写或不可读取，请检查部署目录权限"
	case errors.Is(err, application.ErrIntegrationProbe):
		status, message = http.StatusBadGateway, "连接检查失败，请检查已保存的地址、凭据及读取权限"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status, message = http.StatusRequestTimeout, "请求已取消或超时"
	}
	c.JSON(status, ErrorResponse{Code: status, Message: message})
}
