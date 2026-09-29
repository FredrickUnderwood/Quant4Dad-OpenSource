# Standalone deployment

Quant4Dad runs without a deployment platform, configuration service, private SDK, or predefined domain. Docker with Compose v2.20 or newer and Bash are the host requirements. Prebuilt images include the application and its dependencies; source builds download pinned public Go, npm and Python WASI dependencies. Agent Runtime additionally builds its pinned DeepSeek Harness source tree.

## Docker Hub images

The first Docker Hub release is pending publication. Until a release exists, use the Git/source installation below.

Public image locations: [API](https://hub.docker.com/r/fredrick19/quant4dad-opensource-api), [Web](https://hub.docker.com/r/fredrick19/quant4dad-opensource-web), [MCP](https://hub.docker.com/r/fredrick19/quant4dad-opensource-mcp), [Agent](https://hub.docker.com/r/fredrick19/quant4dad-opensource-agent).

The [download script](../scripts/download.sh) fetches only the installer, Compose file, public image references and license into `./quant4dad`, then pulls the images. It needs curl and Bash; Git, Go, Node.js and a C compiler are not required on the host:

```sh
curl -fsSL https://raw.githubusercontent.com/FredrickUnderwood/Quant4Dad-OpenSource/master/scripts/download.sh -o /tmp/quant4dad-install.sh
bash /tmp/quant4dad-install.sh
```

Set `Q4D_INSTALL_DIR` to change the installation directory, or `Q4D_RELEASE_REF` to select a published Git tag/commit. The downloader fetches every deployment file before updating the installation; it preserves `data/standalone` and refuses to overwrite a Git checkout. Subsequent updates can use the same download command. From the installation directory:

```sh
./scripts/install.sh --pull
# Optional: enable both extensions with the same image release.
./scripts/install.sh --pull --with-mcp --with-agent
```

`--pull` downloads every enabled service image before generating configuration or starting containers. A failed pull stops installation. Re-running the command preserves the enabled extensions, credentials and application data. Keep `data/standalone` intact and back it up before upgrades. A custom state directory must be selected with the same `--state-dir` each time.

From a source checkout, `./scripts/install.sh --pull` uses the public `fredrick19` images in `deploy/images.env`. To select a particular published version:

```sh
./scripts/install.sh --pull --image-prefix docker.io/fredrick19/quant4dad-opensource --image-tag v0.1.0
```

Replace the example tag with an existing release. Four images share the prefix: `-api`, `-web`, `-mcp`, and `-agent`; only enabled services are pulled. `--pull` and `--no-build` are mutually exclusive.

## Build from Git

From the repository root, deploy Web + API with SQLite:

```sh
./scripts/install.sh
```

Open `http://127.0.0.1:3000`. The installer prints the path to the private login token file; read that file and enter its value in the login page. It never prints credentials itself. SQLite, configuration and tool artifacts live under `data/standalone`, outside the images. The default network exposes Web only; API is reachable by Web inside the Compose network.

The installation is single-owner. Generated tokens are random and distinct. Configuration and credential files have mode `0600`, private directories `0700`; containers run with the installation user's non-root UID (root installations use UID 65532). The Web container receives only its own non-secret proxy configuration. Re-running the installer preserves tokens, data, and enabled profiles and recreates containers to apply configuration changes. Do not run two installers against the same state directory concurrently.

No third-party website market-data or news crawlers ship in this repository. Scheduled collection is disabled initially. In Settings, select the official Tushare API with your own token, or a user-provided HTTP service implementing the documented JSON contract. Manual CSV import through the bundled `quant4dad-import` command remains available. See [data providers](data-provider.md), [user settings](settings.md), and the examples directory.

## Optional extensions

The extensions can be enabled independently or together:

```sh
./scripts/install.sh --with-mcp
./scripts/install.sh --with-agent
./scripts/install.sh --with-mcp --with-agent
```

MCP listens at `http://127.0.0.1:8090/mcp`. Set the client's `Authorization: Bearer ...` header using `data/standalone/config/mcp-token`. The dedicated token authorizes all 31 Agent tools, including writes. MCP has its own sessions, audit identity and signing key; it works with Agent Runtime disabled. The API owns database changes and isolated Python execution; the MCP container is a protocol proxy with no database credentials. Clients must preserve `Mcp-Session-Id` and reuse the same JSON-RPC ID and arguments for retries.

Agent Runtime is an optional sidecar sharing the API network namespace. Its Bridge stays on loopback; no Runtime port is published. A separate private configuration contains control, internal MCP and Bridge tokens, an Ed25519 run key, profile revisions and input-meter hash. The installer obtains the actual local image ID and records it as a local Docker image ID, without claiming a signed registry release. Runtime model snapshots live on tmpfs. Internal Agent calls continue to enforce approval and run-capability policies independently of the external MCP token.

Enable Agent, then configure a model provider through Web settings before starting a conversation. No model credentials are needed for base, MCP, or Runtime startup. Model requests use the configured provider and may incur its normal usage charges.

## MySQL and existing databases

Supply the DSN through a private file, rather than putting it in shell arguments:

```sh
./scripts/install.sh --mysql-dsn-file /secure/mysql-dsn
```

A DSN has the driver's usual format, for example `user:password@tcp(mysql-host:3306)/quant4dad?charset=utf8mb4&parseTime=true&loc=Local`. The installer copies the value into private API YAML; it never contacts the database itself. Normal API startup applies schema migrations and creates the default cost model.

For an already migrated database, explicitly skip both operations:

```sh
./scripts/install.sh --mysql-dsn-file /secure/mysql-dsn --skip-migration --skip-seed --no-background
```

This checks the existing schema and fails if it is incomplete. `--no-background` also stops scheduled collection, coverage scans and Agent workers. These flags prevent automatic startup writes; they do not turn the application into a read-only service. Use read-only API requests for a read-only production regression. Never point `QUANT4DAD_TEST_MYSQL_DSN` at a business database: repository tests modify schema and settings. Use a dedicated test database in the same MySQL instance.

The API also supports explicit one-off `--migration check`, `--migration apply`, and `--migration compatible` commands. `check` returns 0 for a complete schema, 10 when migration is needed, and 1 on errors. `compatible` checks an existing schema without changing it.

## Ports, separate installations and updates

```sh
./scripts/install.sh --web-port 18080 --mcp-port 18090 \
  --state-dir /opt/quant4dad-opensource/state \
  --project-name quant4dad-opensource
```

Use `--bind 0.0.0.0` to listen beyond loopback. For Internet access, put an HTTPS reverse proxy in front of Web and MCP. Set `web.trusted_proxies` to the trusted ingress addresses, and `server.trusted_proxies` to the Web proxy addresses; each hop verifies its immediate peer before honoring `X-Forwarded-Proto`. Do not publish the internal API or Agent Bridge. The full MCP endpoint rejects browser `Origin` headers.

Deployment settings are saved in `data/standalone/deployment.env`; startup configuration is in `config/api.yaml`. API Settings saves data-source and OSS integrations in `data/settings/integrations.yaml` under the state directory (override with startup `settings.path`). Its owner must match the API user, directory mode must be `0700`, and file mode `0600`; symlink files/directories are rejected. The API writes atomically and checks revisions to prevent stale browser tabs from overwriting one another. Do not share one private settings file between multiple API processes or edit it while the API is running. It takes precedence over the corresponding startup YAML defaults. Reinstallation preserves this data directory.

Existing collection tasks keep their original client/configuration snapshot. New tasks use the saved settings. An archive run blocks changes to its archive configuration until it finishes; no new automatic work is started by settings when `--no-background` is active. The Settings page includes a read-only deployment checklist for options that still require installation or restart.

Review and edit startup files locally as needed. Avoid `docker compose config` without `--quiet` when handling private overrides. Use the printed `docker compose --env-file ... -f ...` command to inspect services or logs. `down` stops the installation; it does not delete the bind-mounted state directory.

`--no-build` uses already loaded images. Default image names are `quant4dad-opensource-api:local`, `quant4dad-opensource-web:local`, `quant4dad-opensource-mcp:local`, and `quant4dad-opensource-agent:local`. Set their `Q4D_*_IMAGE` environment variables on the first install, or edit the saved deployment settings on subsequent installs. Updating uses the same install command against the new source; back up database and state before an upgrade.

## Verification

Installer image selection and pull-failure checks run without a Docker daemon:

```sh
python3 scripts/test-install.py
```

A read-only smoke check verifies login, Web-to-API proxying, core query routes, optional Agent routes, and the exact MCP catalog:

```sh
python3 scripts/standalone-smoke.py \
  --web-url http://127.0.0.1:3000 \
  --token-file data/standalone/config/login-token

python3 scripts/standalone-smoke.py \
  --web-url http://127.0.0.1:3000 \
  --token-file data/standalone/config/login-token \
  --mcp-url http://127.0.0.1:8090/mcp \
  --mcp-token-file data/standalone/config/mcp-token --expect-agent
```

Use `--expect-agent` only when Runtime is enabled. The final command covers the combined profile. Test all four combinations in separate state directories, or enable them in sequence while retaining the same state.

For a real Agent check, use a separate test installation with Agent enabled, import the synthetic CSV examples, and configure an authorized model provider in that installation. This runner makes actual model requests (and one capability probe if needed), so normal provider charges apply. Use the provider and default-model identifiers shown in the model catalog:

```sh
python3 scripts/standalone-agent-smoke.py \
  --web-url http://127.0.0.1:3000 \
  --token-file data/standalone/config/login-token \
  --provider YOUR_PROVIDER --model YOUR_MODEL --timeout 180
```

The runner submits one read-only research request. It verifies an actual internal `list_instruments(size=1)` call against the database, its successful audit record, and a final answer citing the returned code. It rejects extra tools, approvals and invented results. It archives its own Session, cancels its own unfinished Run on failure or timeout, and emits only bounded JSON evidence, without credentials or conversation text. It does not change provider configuration or retry a submitted Run. Keep this model check off shared business installations.

Re-running the installer preserves credentials, stored data, enabled profiles, and API/Web business settings. Agent connection and profile configuration is regenerated from the current image; review any custom Agent changes after an update.

`scripts/mcp-live-test.py` exercises every tool against a database containing imported bars and event/news records. It creates specifically named Smoke strategies, pipelines and a small backtest; it does not modify existing objects or deliver notifications. An empty database cannot satisfy individual event/news reads, so the script reports missing happy-path coverage rather than claiming all tools succeeded.

## Publishing Docker releases

Configure the GitHub repository variable `DOCKERHUB_USERNAME=fredrick19`, secret `DOCKERHUB_TOKEN` (write access to the four image repositories), and optionally `DOCKERHUB_NAMESPACE` for an organization. The `quant4dad-opensource-api`, `-web`, `-mcp`, and `-agent` repositories must be public for anonymous installation.

Run **Publish Docker images** with a stable version such as `v0.1.0`, or push that version tag. The workflow builds on native amd64 and arm64 runners, checks Web/API/MCP/Agent startup and the 31-tool catalog, then publishes the version and `latest` image tags. It attaches a small installer archive and SHA256 file to the GitHub release. A failed build or smoke check prevents publication of the combined release tags and download bundle.

Workflow releases additionally provide [quant4dad-docker.tar.gz](https://github.com/FredrickUnderwood/Quant4Dad-OpenSource/releases/latest/download/quant4dad-docker.tar.gz), with version-pinned references. Extract it over the installation directory and run `./scripts/install.sh --pull`.

The archive includes version-pinned image names in `deploy/images.env`, installer, Compose configuration and license; it contains no source build context, credentials or user data. Local packaging is also available:

```sh
bash scripts/package-docker-release.sh docker.io/fredrick19/quant4dad-opensource v0.1.0 quant4dad-docker.tar.gz
```
