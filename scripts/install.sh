#!/usr/bin/env bash
set -euo pipefail
umask 077
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
state="${Q4D_STATE_DIR:-$root/data/standalone}"
with_mcp=0 with_agent=0 build=1 mysql_file=""
# First locate the state directory before loading its persisted deployment options.
for arg in "$@"; do if [[ "$arg" == --help || "$arg" == -h ]]; then
  printf "%s\n" "Usage: scripts/install.sh [--with-mcp] [--with-agent] [--mysql-dsn-file FILE]" "       [--skip-migration] [--skip-seed] [--no-background] [--web-port 3000] [--mcp-port 8090]" "       [--bind 127.0.0.1] [--state-dir DIR] [--project-name NAME] [--no-build]"
  exit 0
fi; done
argument_count=$#
args=("$@")
while (($#)); do
  case "$1" in
    --state-dir) [[ $# -ge 2 ]] || exit 2; state="$2"; shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$state"
state="$(cd "$state" && pwd -P)"
if [[ -f "$state/deployment.env" ]]; then
  while IFS='=' read -r key value; do
    case "$key" in
      Q4D_UID|Q4D_GID|Q4D_BIND|Q4D_WEB_PORT|Q4D_MCP_PORT|Q4D_PROJECT_NAME|Q4D_API_IMAGE|Q4D_WEB_IMAGE|Q4D_MCP_IMAGE|Q4D_AGENT_IMAGE|Q4D_SKIP_MIGRATION|Q4D_SKIP_SEED|Q4D_NO_BACKGROUND|Q4D_TIMEZONE) export "$key=$value" ;;
    esac
  done < "$state/deployment.env"
fi
if ((argument_count)); then set -- "${args[@]}"; else set --; fi
while (($#)); do
  case "$1" in
    --with-mcp) with_mcp=1; shift ;;
    --with-agent) with_agent=1; shift ;;
    --no-build) build=0; shift ;;
    --skip-migration) export Q4D_SKIP_MIGRATION=true; shift ;;
    --skip-seed) export Q4D_SKIP_SEED=true; shift ;;
    --no-background) export Q4D_NO_BACKGROUND=true; shift ;;
    --state-dir) shift 2 ;;
    --mysql-dsn-file) [[ $# -ge 2 ]] || exit 2; mysql_file="$2"; shift 2 ;;
    --web-port) [[ $# -ge 2 ]] || exit 2; export Q4D_WEB_PORT="$2"; shift 2 ;;
    --mcp-port) [[ $# -ge 2 ]] || exit 2; export Q4D_MCP_PORT="$2"; shift 2 ;;
    --bind) [[ $# -ge 2 ]] || exit 2; export Q4D_BIND="$2"; shift 2 ;;
    --project-name) [[ $# -ge 2 ]] || exit 2; export Q4D_PROJECT_NAME="$2"; shift 2 ;;
    --help|-h) printf '%s\n' 'Usage: scripts/install.sh [--with-mcp] [--with-agent] [--mysql-dsn-file FILE]' '       [--skip-migration] [--skip-seed] [--no-background] [--web-port 3000] [--mcp-port 8090]' '       [--bind 127.0.0.1] [--state-dir DIR] [--project-name NAME] [--no-build]'; exit 0 ;;
    *) printf 'Unknown option: %s\n' "$1" >&2; exit 2 ;;
  esac
done
command -v docker >/dev/null || { echo 'Docker is required.' >&2; exit 1; }
docker compose version >/dev/null
docker info >/dev/null
export Q4D_STATE_DIR="$state"
export Q4D_UID="${Q4D_UID:-$(id -u)}" Q4D_GID="${Q4D_GID:-$(id -g)}"
if [[ "$Q4D_UID" == 0 ]]; then export Q4D_UID=65532 Q4D_GID=65532; fi
export Q4D_BIND="${Q4D_BIND:-127.0.0.1}" Q4D_WEB_PORT="${Q4D_WEB_PORT:-3000}" Q4D_MCP_PORT="${Q4D_MCP_PORT:-8090}"
export Q4D_PROJECT_NAME="${Q4D_PROJECT_NAME:-quant4dad-opensource}"
export Q4D_API_IMAGE="${Q4D_API_IMAGE:-quant4dad-opensource-api:local}" Q4D_WEB_IMAGE="${Q4D_WEB_IMAGE:-quant4dad-opensource-web:local}"
export Q4D_MCP_IMAGE="${Q4D_MCP_IMAGE:-quant4dad-opensource-mcp:local}" Q4D_AGENT_IMAGE="${Q4D_AGENT_IMAGE:-quant4dad-opensource-agent:local}"
export Q4D_SKIP_MIGRATION="${Q4D_SKIP_MIGRATION:-false}" Q4D_SKIP_SEED="${Q4D_SKIP_SEED:-false}" Q4D_NO_BACKGROUND="${Q4D_NO_BACKGROUND:-false}"
export Q4D_TIMEZONE="${Q4D_TIMEZONE:-Asia/Shanghai}"
[[ "$Q4D_TIMEZONE" =~ ^[A-Za-z0-9_+./-]+$ && "$Q4D_TIMEZONE" != *..* && "$Q4D_TIMEZONE" != /* ]] || { echo 'Invalid deployment timezone' >&2; exit 2; }
[[ "$Q4D_UID" =~ ^[1-9][0-9]*$ && "$Q4D_GID" =~ ^[0-9]+$ && "$Q4D_PROJECT_NAME" =~ ^[a-z0-9][a-z0-9_-]*$ ]] || exit 2
for port in "$Q4D_WEB_PORT" "$Q4D_MCP_PORT"; do [[ "$port" =~ ^[0-9]+$ ]] && ((port>0 && port<65536)) || exit 2; done
[[ "$Q4D_BIND" =~ ^[0-9.]+$ ]] || { echo '--bind requires an IPv4 address' >&2; exit 2; }
chmod 700 "$state"
if [[ $(id -u) == 0 ]]; then chown "$Q4D_UID:$Q4D_GID" "$state"; fi
if [[ -f "$state/profiles" ]]; then
 while IFS= read -r profile; do case "$profile" in mcp) with_mcp=1 ;; agent) with_agent=1 ;; '') ;; *) exit 2 ;; esac; done < "$state/profiles"
fi
COMPOSE_PROFILES=""
if ((with_mcp)); then COMPOSE_PROFILES=mcp; fi
if ((with_agent)); then COMPOSE_PROFILES="${COMPOSE_PROFILES:+$COMPOSE_PROFILES,}agent"; fi
export COMPOSE_PROFILES
# This file contains only deployment settings. Business secrets stay in private YAML/token files.
: > "$state/deployment.env"
for key in Q4D_UID Q4D_GID Q4D_BIND Q4D_WEB_PORT Q4D_MCP_PORT Q4D_PROJECT_NAME Q4D_STATE_DIR Q4D_API_IMAGE Q4D_WEB_IMAGE Q4D_MCP_IMAGE Q4D_AGENT_IMAGE Q4D_SKIP_MIGRATION Q4D_SKIP_SEED Q4D_NO_BACKGROUND Q4D_TIMEZONE COMPOSE_PROFILES; do
 value="${!key}"; [[ "$value" != *$'\n'* && "$value" != *$'\r'* ]] || exit 2; printf '%s=%s\n' "$key" "$value" >> "$state/deployment.env"
done
compose=(docker compose --env-file "$state/deployment.env" -f "$root/deploy/compose.yaml")
if ((build)); then
 "${compose[@]}" build api
 "${compose[@]}" build web
 ((with_mcp==0)) || "${compose[@]}" build mcp
 ((with_agent==0)) || "${compose[@]}" build agent
fi
if ((with_agent)); then
 digest="$(docker image inspect --format '{{.Id}}' "$Q4D_AGENT_IMAGE")"
 docker run --rm --network none --user "$Q4D_UID:$Q4D_GID" --entrypoint node \
   --mount "type=bind,source=$state,target=/setup" "$Q4D_AGENT_IMAGE" \
   /opt/q4d/agent-runtime/scripts/standalone-config.mjs /setup/agent --image-digest "$digest"
fi
setup=(docker run --rm --network none --user "$Q4D_UID:$Q4D_GID" --mount "type=bind,source=$state,target=/setup" -e "Q4D_SETUP_MCP=$with_mcp" -e "Q4D_SETUP_AGENT=$with_agent")
if [[ -n "$mysql_file" ]]; then
 [[ -f "$mysql_file" ]] || { echo 'MySQL DSN file does not exist.' >&2; exit 2; }
 mysql_file="$(cd "$(dirname "$mysql_file")" && pwd -P)/$(basename "$mysql_file")"
 # Use an owner-matched private copy so a non-root setup container can read a root-owned source.
 cp "$mysql_file" "$state/mysql-input"
 chmod 600 "$state/mysql-input"
 if [[ $(id -u) == 0 ]]; then chown "$Q4D_UID:$Q4D_GID" "$state/mysql-input"; fi
 trap 'rm -f "$state/mysql-input"' EXIT
 setup+=(-e Q4D_SETUP_MYSQL_DSN_FILE=/setup/mysql-input)
fi
"${setup[@]}" "$Q4D_API_IMAGE" --setup /setup
rm -f "$state/mysql-input"
"${compose[@]}" up -d --no-build --force-recreate --wait --wait-timeout 180
printf 'Web: http://%s:%s\nLogin token file: %s/config/login-token\n' "$Q4D_BIND" "$Q4D_WEB_PORT" "$state"
if ((with_mcp)); then printf 'MCP: http://%s:%s/mcp\nMCP token file: %s/config/mcp-token\n' "$Q4D_BIND" "$Q4D_MCP_PORT" "$state"; fi
printf 'Manage: docker compose --env-file %q -f %q ps\n' "$state/deployment.env" "$root/deploy/compose.yaml"
