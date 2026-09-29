# Quant4Dad OpenSource 架构

本文对应代码版本 `66af9ec`，描述当前实现，不代表尚未实现的接口或未来规划。七张图与本文放在同一目录；更新入口、工具目录或进程边界时，应一并更新相关图和说明。

[Figma 架构图源文件](https://www.figma.com/design/Bv58gGtjRlUHcKsOfl9J8F/?node-id=39-2)

| 图 | 高清 PNG | 可编辑 Figma |
| --- | --- | --- |
| 四模块与部署总览 | [图片](01-overview.png) | [画板](https://www.figma.com/design/Bv58gGtjRlUHcKsOfl9J8F/?node-id=40-2) |
| Web 与 MCP 入口 | [图片](02-entry.png) | [画板](https://www.figma.com/design/Bv58gGtjRlUHcKsOfl9J8F/?node-id=40-102) |
| API 业务组件 | [图片](03-api.png) | [画板](https://www.figma.com/design/Bv58gGtjRlUHcKsOfl9J8F/?node-id=43-2) |
| Agent Runtime 与权限 | [图片](04-agent.png) | [画板](https://www.figma.com/design/Bv58gGtjRlUHcKsOfl9J8F/?node-id=43-68) |
| 策略开发与回测 | [图片](05-strategy-backtest.png) | [画板](https://www.figma.com/design/Bv58gGtjRlUHcKsOfl9J8F/?node-id=45-2) |
| 事件流水线 | [图片](06-event-pipeline.png) | [画板](https://www.figma.com/design/Bv58gGtjRlUHcKsOfl9J8F/?node-id=45-106) |
| 数据同步与覆盖率 | [图片](07-data-sync.png) | [画板](https://www.figma.com/design/Bv58gGtjRlUHcKsOfl9J8F/?node-id=45-214) |

图中卡片边框与流程连线统一为：**Web 绿色、外部 MCP 紫色、内置 Agent 蓝色、共享业务与存储中性色**。大块浅色背景表示架构层级或流程阶段，同一层内的小卡片表示具体组件与步骤。技术标志取自 React、MCP、Go、MySQL、SQLite、DSH 与 Node.js 的官方资源，见 [图标来源](icons/README.md)。同一业务能力可由不同入口调用，但不意味着鉴权方式或接口范围相同。

## 01 · 总体架构

![总体架构：四个进程、可选扩展与共享存储](01-overview.png)

系统有四个独立进程，标准安装默认启动 Web 和 API。MCP 与 Agent Runtime 是独立可选扩展，可单独启用，也可同时启用。

| 模块 | 职责 | 部署关系 |
| --- | --- | --- |
| Web | 提供前端静态资源，代理 `/api/*`，转发 SSE | 默认启动；不直接访问业务数据库 |
| API | HTTP 接口、业务服务、任务调度、工具执行、权限与持久化 | 默认启动；业务组件主要在这个 Go 进程内 |
| MCP | 外部 MCP HTTP 入口，校验专用 token，并代理到 API | 可选 `mcp` profile；完整工具模式不直接访问数据库 |
| Agent Runtime | 会话运行、模型调用、工具调度、运行事件与对话状态 | 可选 `agent` profile；Node 进程内嵌 DSH/Cordis |

默认使用 SQLite，可配置 MySQL。启动配置来自本地私有文件；不依赖 Agenda 或 Nacos。API 持有业务数据和工具文件，Runtime 持有独立会话状态，模型服务、行情提供方和 OSS 是按需使用的外部依赖。API 内的 worker、采集器、流水线引擎和 Python VM 不是独立微服务。

源码：[Compose 服务与挂载](../../deploy/compose.yaml)、[API 装配](../../cmd/quant4dad/main.go)、[配置](../../config/config.go)、[安装脚本](../../scripts/install.sh)。

## 02 · 入口与信任边界

![入口：Web、外部 MCP、内置 Agent 的不同权限路径](02-entry.png)

Web 经登录认证访问 `/api/v1/*`。外部 MCP 客户端访问 MCP 进程的 `/mcp`，后者只转发到 API `/external/mcp`；专用 Bearer token 与网页登录、内部 Agent 凭据分离。初始化后使用 `Mcp-Session-Id`，稳定的 JSON-RPC 请求 ID 参与调用幂等校验。持有该外部 token 已获全部已发布工具的权限，包括写工具；它不要求内置 Agent 的逐次交互审批。

内置 Agent 通过 API 创建 Session/Run，再由 Runtime 使用 `/internal/mcp` 执行工具。该入口同时校验内部 MCP token、签名 Run capability、当前授权、profile、工具范围与预算；R2/R3 写操作需要对应的一次性审批 receipt。外部 MCP 不接受内部权限头，模型也不能在工具参数中自行提供 authority。

标准 Compose 仅发布 Web/MCP 端口，默认绑定宿主机回环地址。Runtime 与 API 共享网络命名空间，以回环地址通信，但仍是不同容器、不同进程和不同文件系统；浏览器不直连 Runtime。Web 只代理 `/api/*`，不会把内部 bootstrap 或 MCP 网关暴露成浏览器路由。

源码：[Web 代理](../../internal/webui/server.go)、[MCP 代理](../../cmd/quant4dad-mcp/proxy.go)、[外部协议适配器](../../internal/mcp/external_http.go)、[外部授权](../../internal/application/external_mcp_app.go)、[内部网关](../../internal/application/agent_tool_gateway_app.go)。

## 03 · API 内部组件与工具范围

![API 内部：业务组件、共享工具目录和持久化](03-api.png)

HTTP handler 调用 application 编排层，再调用 service 和 repository。策略、回测、流水线、事件、行情同步、设置和归档都在 API 内完成；它们共享数据库及相应文件仓库。行情和 OSS 连接设置通过私有配置文件持久化并应用到运行状态，模型设置通过业务存储管理。OSS 用于已有事件归档：上传不可覆盖批次、读取校验后，仅删除仍与导出快照一致的终态记录。

内部 Agent 网关与外部 MCP 共用启动时创建的 `ToolCatalogApplication`。三个 profile 的目录分别为 `research` 12 个、`strategy_lab` 19 个、`pipeline_builder` 12 个；外部 MCP 暴露去重后的 **31 个工具**。工具按真实依赖是否可用发布，例如 Python 运行时不可用时不会发布 `execute_python`。具体名称以目录源码和 `tools/list` 为准。

这个目录不是全部 HTTP API 的镜像：它覆盖行情研究、资讯/事件读取、策略验证与保存、回测、流水线定义和安全预览；不提供数据采集、设置修改、OSS 操作、策略/流水线删除或真实事件注入工具。

`execute_python` 在 **Go API 内的 wazero WebAssembly VM** 执行固定 CPython WASI，输入为本会话授权的行情文件；它不是 Runtime 的宿主机 Python，也不提供任意宿主文件、网络或 subprocess 访问。工具审计和结果 artifact 同样由 API 管理。

源码：[共享目录](../../internal/application/agent_p0_catalog.go)、[完整 profile 工具名单](../../internal/application/agent_research_catalog.go)、[工具执行约束](../../internal/application/tool_catalog_app.go)、[Python VM](../../internal/pythonexec/runtime.go)、[动态设置](../../internal/application/integration_settings_app.go)、[归档](../../internal/application/archive.go)。

## 04 · 内置 Agent 生命周期

![Agent 生命周期：API 绑定、Runtime 执行、内部 MCP 与 SSE](04-agent.png)

浏览器创建 Session、发送消息后，API 持久化会话绑定和 Run 投递/授权索引，并固定模型版本、profile、工具目录和预算等执行上下文，签发 Run capability。SQL 中的 `AgentRequestBinding` 记录请求身份、投递确认和授权信息，不是 Run 执行状态机；**Runtime 的 JSONL/journal 是会话与 Run 执行状态的事实来源**。Go `agentbridge` 使用 bridge token 把会话和 prompt 提交给 Runtime；Node Runtime 验证当前授权后，由内嵌 DSH 执行模型循环。API 的 Run reconciler 协调投递绑定，并不执行主对话模型循环或另建执行状态机。

Runtime 反向使用 control token 从 API 获取 bootstrap 模型快照和当前 Run authorization。Runtime 将模型配置和凭据写入私有 tmpfs，原子发布为代际快照；消费者固定读取同一代文件，更新时发布新一代，已发布的一代不原地修改。这是运行时写入的私有快照，不是只读挂载；凭据不经过浏览器或模型工具参数。模型提出工具调用后，Runtime 的可信适配器附加 Run 权限并访问内部 MCP；需要审批时，Web/API 决策后通过 bridge 通知 Runtime，以相同调用身份继续执行。

运行事件先进入 Runtime 的持久 journal，再经 `agentbridge → API /api/v1/agent/runs/:runID/events → Web → 浏览器` 形成 SSE。断开 SSE 订阅不等于取消 Run。主对话和模型 probe 的模型请求在 Runtime 执行；自动会话标题和流水线 AI 节点是例外，它们由 Go API 直接调用模型。

源码：[Session 编排](../../internal/application/agent_session_app.go)、[Run 投递索引](../../internal/domain/agent_run.go)、[Run 编排](../../internal/application/agent_run_app.go)、[Runtime 会话执行](../../agent-runtime/src/runtime/session-host.ts)、[模型快照](../../agent-runtime/src/bootstrap/sync.mjs)、[tmpfs 代际写入](../../agent-runtime/src/bootstrap/snapshot-store.mjs)、[可信工具调用](../../agent-runtime/src/mcp/trusted-gateway.mjs)、[SSE handler](../../internal/handler/agent_run_handler.go)。

## 05 · 策略开发与回测

![策略开发与回测：验证、保存、异步计算与结果查询](05-strategy-backtest.png)

工具链为：`list_indicators` / 行情查询 → `validate_strategy` → `create_strategy` 或 `update_strategy` → `list_cost_models` → `run_backtest` → 查询任务与报告。验证成功返回的 `validation_id` 绑定当前会话、完整定义和有效期；保存时必须携带该凭据，修改任何定义字段后需重新验证。更新还要提供 `expected_version`。内置 Agent 保存策略需要 R2 审批；外部 MCP 使用其专用 token 授权。

`run_backtest` 是 R1 工具，固定策略和费用快照，返回 `job_id` 后由 API 的持久任务 worker 异步计算；不经过 Runtime 计算，也不进行真实交易。Web 的普通回测 API 则经 `BacktestApp.Enqueue` 启动 API 内受并发限制的任务，不能把两条入口都画成同一套持久领取机制。

工具通过 `get_backtest_job` / `list_backtests` 查询状态，`get_backtest_report` 只返回指标摘要。完整交易明细与净值序列由 Web/API 提供；目录没有删除策略工具。

源码：[验证工具](../../internal/application/catalog_validation_tools.go)、[保存工具](../../internal/application/catalog_write_tools.go)、[回测工具](../../internal/application/catalog_backtest_tool.go)、[工具回测 worker](../../internal/application/agent_backtest_worker.go)、[Web 回测编排](../../internal/application/backtest_app.go)、[查询工具范围](../../internal/application/catalog_query_tools.go)。

## 06 · 事件流水线

![事件流水线：定义与预览、真实事件执行、结果存储](06-event-pipeline.png)

工具可调用 `get_pipeline_node_types`、`list_pipelines`、`get_pipeline`、`validate_pipeline`、`create_pipeline`、`update_pipeline`、`dry_run_pipeline_safe` 和 `set_pipeline_status`。创建仅保存 `draft`；更新保留启停状态。内置 Agent 启用流水线或更新已启用流水线需要 R3 审批，停用为 R2；写操作使用版本校验。

必须区分两条执行链路：`dry_run_pipeline_safe` 对未保存定义和 `sample_event` 做预览，不保存事件、不发送邮件或飞书，但 AI 节点可能真实调用已配置模型；真实事件通过 Web/API 的 `POST /api/v1/events/ingest/:pipelineId`，或已配置的资讯采集订阅，进入已启用流水线，由 API 执行节点并保存事件、节点轨迹和 AI 结果。

MCP/Agent 可用 `list_events`、`get_event` 查询已有事件，**没有真实事件注入、触发或删除流水线工具**。启用流水线也不等于立刻注入一条事件。流水线 AI 节点由 Go API 调用模型，不依赖可选 Agent Runtime。

源码：[工具定义与风险](../../internal/application/catalog_write_tools.go)、[预览服务](../../internal/service/pipeline_service.go)、[HTTP 入口](../../internal/handler/pipeline_handler.go)、[真实事件执行](../../internal/application/pipeline_app.go)、[资讯分发](../../internal/application/news_collector.go)、[AI 节点](../../internal/pipeline/nodes/ai.go)。

## 07 · 数据同步

![数据同步：配置、采集任务、外部提供方与行情库](07-data-sync.png)

Web 保存数据源及自动采集配置后，手动 `POST /api/v1/datasync` 请求或每日调度进入 `DataSyncApp.Enqueue`。每个已接纳任务在 API 内存中固定自己的 client/config 快照，并发拉取数据，写入标的和 K 线库，同时持久化任务进度及失败项。client/config 快照不写入任务记录，当前实现不保证进程重启后续跑。当前可配置 Tushare 官方 API 或用户自有 HTTP 数据接口；未配置可用 provider 时拒绝创建采集任务，也可通过独立 CSV 导入入口使用自有数据。

MCP/Agent 在这张图上只连接“读取已有行情”：`list_instruments`、`get_instrument`、`query_kline`、`read_kline_file`、`analyze_kline`、`execute_python`、`latest_bar_date`、`get_data_coverage`。**它们不触发采集、不修改数据源或自动任务设置**。任务创建、任务/失败查询、覆盖扫描和设置保存是独立 Web/API 能力。

覆盖率有两条不同的数据路径：Web 覆盖率面板读取 `CoverageScanner` 写入的 `data_coverage_summary`，手动扫描入口为 `POST /api/v1/datasync/coverage/scan`；MCP/Agent 的 `get_data_coverage` 实时查询指定标的和周期的 K 线条数、首末日期，不读取该汇总表，也不触发扫描。条数和首末日期不证明区间内交易日数据完整。

行情采集与 OSS 事件归档是两条独立业务链路，不能把 OSS 画成行情采集的必经步骤。自动任务遵循部署后台任务开关，页面保存启用配置不会越过该开关。

源码：[同步 HTTP 接口](../../internal/handler/datasync_handler.go)、[任务快照与采集](../../internal/application/datasync_app.go)、[每日调度](../../internal/application/auto_sync.go)、[覆盖率汇总扫描](../../internal/application/coverage_scanner.go)、[实时标的覆盖查询](../../internal/repository/bar_coverage_repository.go)、[提供方注册](../../internal/repository/datasource/registry.go)、[提供方实现](../../internal/repository/datasource/providers)、[设置接口](../../internal/handler/integration_setting_handler.go)、[CSV 导入](../../cmd/quant4dad-import)。
