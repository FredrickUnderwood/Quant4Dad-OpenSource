# Quant4Dad Open Source

一个可以独立部署的量化研究工作台：行情管理与分析、可校验的策略、回测、事件流水线，以及可选的 MCP 和 Agent Runtime。

本仓库不包含任何内置行情或资讯爬虫。你可以导入自己有权使用的 CSV 数据，或实现保留的数据源接口。默认使用 SQLite，不需要配置中心或运维平台。

## 一行启动

准备 Docker Engine / Docker Desktop、Docker Compose v2.20 或更新版本及 Git，然后执行：

```bash
git clone https://github.com/FredrickUnderwood/Quant4Dad-OpenSource.git && cd Quant4Dad-OpenSource && ./scripts/install.sh
```

首次运行会构建镜像、生成独立登录凭据和本地配置，并等待 Web + API 就绪。浏览器打开 `http://127.0.0.1:3000`。登录 token 保存在 `data/standalone/config/login-token`，安装器只显示文件位置，不把它写入日志。只需读取该文件并在登录页输入。

重复运行安装器会保留已有凭据、数据、启用的扩展，以及 API 和 Web 的业务配置。Agent 连接参数和 profile 配置会按当前镜像重新生成；如有自定义调整，请在升级后重新核对。默认绑定本机地址；更改端口可传 `--web-port 8080`，需要由其他机器访问时显式设置 `--bind 0.0.0.0`，并在自己的入口配置 HTTPS。

## 可选扩展

| 安装方式 | 启用服务 |
| --- | --- |
| `./scripts/install.sh` | Web + API |
| `./scripts/install.sh --with-mcp` | Web + API + MCP |
| `./scripts/install.sh --with-agent` | Web + API + Agent Runtime |
| `./scripts/install.sh --with-mcp --with-agent` | 全部服务 |

MCP 可以独立于 Agent Runtime 使用，默认地址为 `http://127.0.0.1:8090/mcp`。使用 `Authorization: Bearer <mcp-token>`，独立 token 位于 `data/standalone/config/mcp-token`。它授权全部 31 个业务工具，包括写入策略、运行回测和启停流水线。连接时建立 MCP 会话，重试写调用时复用原会话和 JSON-RPC ID。

Agent Runtime 启用后，在 Web 设置页配置自己的模型接口和密钥。模型凭据不是基础部署的前提。内部 Agent 的能力校验、预算和写操作审批继续生效。

## 接入数据

首次安装不抓取网络行情。提供了纯生成的演示 CSV，它们不是实际行情：

```bash
mkdir -p data/standalone/data/import
cp examples/import/*.csv data/standalone/data/import/
docker compose --env-file data/standalone/deployment.env -f deploy/compose.yaml exec -T api /app/quant4dad-import -config /app/config/api.yaml -instruments /app/data/import/instruments.csv -bars /app/data/import/bars.csv -dry-run
```

确认检查结果后，去掉 `-dry-run` 执行导入。相同数据可以重复导入；已有不同数据默认报冲突，只有明确传入 `-replace` 才覆盖。导入先完整验证，再一次事务提交。详见 [数据格式及导入说明](docs/data-import.md)。

已有 MySQL 可通过私有 DSN 文件接入：

```bash
./scripts/install.sh --mysql-dsn-file /私有路径/mysql-dsn
```

如果连接的是已有运行中的业务库，只做读取回归时使用 `--skip-migration --skip-seed --no-background`，避免启动迁移、初始化数据和后台任务。这些启动选项不是 HTTP 只读权限；不要对共享业务库运行会写入的回归用例。

## 保留的能力

- 股票/ETF 标的与 K 线查询、指标、覆盖度、会话文件、隔离 Python 分析。
- 策略配置及 Starlark 脚本检查、版本控制、异步回测和报告。
- 可视化事件流水线、用户事件接入、可选模型与通知节点。
- 31 项 MCP 业务工具，与 Agent 使用同一业务实现；独立 token 和会话隔离。
- 可选 Agent Runtime、模型管理、会话、工具调用审计和写操作审批。

数据源接口位于 `internal/repository/datasource`。`providers` 包默认为空，供用户注册自己的实现；没有安装 provider 时，同步请求会明确返回不可用，已有数据查询与回测不受影响。历史数据的复权来源标记仍保留兼容。

## 配置、运维和开发

生成的私有配置位于 `data/standalone/config/`，运行时只读取本地 YAML。修改服务配置后重启服务。SQLite、工具文件和 Agent 状态都位于 `data/standalone` 下，备份这个目录即可保留本机数据和凭据；使用 MySQL 时另行备份数据库。

[独立部署与回归说明](docs/deployment.md) 包含配置布局、MySQL、四种扩展组合、健康检查和日常操作。

开发环境使用 Go 1.26.8、Node.js 24.20.x；版本和依赖均在仓库固定：

```bash
go test ./...
go build ./...
go vet ./...
cd web && npm ci && npm test && npm run build
```

`config/quant4dad.yaml` 是仅监听本机的开发模板；正式安装请使用安装器生成的私有配置。不要提交数据库、运行目录、token、模型密钥或生产配置。

## 许可证

项目代码采用 [MIT License](LICENSE)。保留的 Python WASI 与图标许可分别见 [Python 许可](third_party/python-wasi/LICENSE.txt)、[图标许可](docs/licenses/lobe-icons-LICENSE.txt)。Agent Runtime 的上游依赖版本见 `agent-runtime/upstream.lock.json`，构建时从其公开来源获取。
