#!/usr/bin/env bash
set -euo pipefail
umask 077
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
state="${Q4D_STATE_DIR:-$root/data/standalone}"
with_mcp=0 with_agent=0 build=1 pull=0 mysql_file=""
image_prefix="" image_tag=latest
existing_install=0 saved_storage="" saved_project=""
usage() {
  printf '%s\n' \
    'Usage: scripts/install.sh [--with-mcp] [--with-agent] [--mysql-dsn-file FILE]' \
    '       [--skip-migration] [--skip-seed] [--no-background] [--web-port 3000] [--mcp-port 8090]' \
    '       [--bind 127.0.0.1] [--state-dir DIR] [--project-name NAME] [--no-build]' \
    '       [--pull [--image-prefix docker.io/OWNER/quant4dad-opensource] [--image-tag TAG]]' \
    '       [--data-storage volume|bind] (new installations default to volume)'
}
# First locate the state directory before loading its persisted deployment options.
for arg in "$@"; do if [[ "$arg" == --help || "$arg" == -h ]]; then
  usage
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
install_lock="$state/.install-lock"
mkdir "$install_lock" 2>/dev/null || { echo 'Another installation is using this state directory (.install-lock exists).' >&2; exit 1; }
trap 'rm -f "$state/mysql-input" "${deployment_candidate:-}"; rmdir "$install_lock"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
if [[ -f "$state/deployment.env" || -f "$state/config/api.yaml" || -f "$state/data/quant4dad.db" ]]; then existing_install=1; fi
if [[ -f "$state/deployment.env" ]]; then
  while IFS='=' read -r key value; do
    case "$key" in
      Q4D_DATA_STORAGE) saved_storage="$value"; export "$key=$value" ;;
      Q4D_PROJECT_NAME) saved_project="$value"; export "$key=$value" ;;
      Q4D_UID|Q4D_GID|Q4D_BIND|Q4D_WEB_PORT|Q4D_MCP_PORT|Q4D_API_IMAGE|Q4D_WEB_IMAGE|Q4D_MCP_IMAGE|Q4D_AGENT_IMAGE|Q4D_SKIP_MIGRATION|Q4D_SKIP_SEED|Q4D_NO_BACKGROUND|Q4D_TIMEZONE|Q4D_DATA_VOLUME) export "$key=$value" ;;
    esac
  done < "$state/deployment.env"
fi
if ((argument_count)); then set -- "${args[@]}"; else set --; fi
while (($#)); do
  case "$1" in
    --with-mcp) with_mcp=1; shift ;;
    --with-agent) with_agent=1; shift ;;
    --no-build) build=0; shift ;;
    --pull) pull=1; shift ;;
    --image-prefix) [[ $# -ge 2 ]] || exit 2; image_prefix="$2"; shift 2 ;;
    --image-tag) [[ $# -ge 2 ]] || exit 2; image_tag="$2"; shift 2 ;;
    --skip-migration) export Q4D_SKIP_MIGRATION=true; shift ;;
    --skip-seed) export Q4D_SKIP_SEED=true; shift ;;
    --no-background) export Q4D_NO_BACKGROUND=true; shift ;;
    --state-dir) shift 2 ;;
    --mysql-dsn-file) [[ $# -ge 2 ]] || exit 2; mysql_file="$2"; shift 2 ;;
    --web-port) [[ $# -ge 2 ]] || exit 2; export Q4D_WEB_PORT="$2"; shift 2 ;;
    --mcp-port) [[ $# -ge 2 ]] || exit 2; export Q4D_MCP_PORT="$2"; shift 2 ;;
    --bind) [[ $# -ge 2 ]] || exit 2; export Q4D_BIND="$2"; shift 2 ;;
    --project-name) [[ $# -ge 2 ]] || exit 2; export Q4D_PROJECT_NAME="$2"; shift 2 ;;
    --data-storage) [[ $# -ge 2 ]] || exit 2; export Q4D_DATA_STORAGE="$2"; shift 2 ;;
    --help|-h) usage; exit 0 ;;
    *) printf 'Unknown option: %s\n' "$1" >&2; exit 2 ;;
  esac
done
if [[ -z "${Q4D_DATA_STORAGE:-}" ]]; then
  if ((existing_install)); then export Q4D_DATA_STORAGE=bind; else export Q4D_DATA_STORAGE=volume; fi
fi
[[ "$Q4D_DATA_STORAGE" == volume || "$Q4D_DATA_STORAGE" == bind ]] || { echo '--data-storage must be volume or bind.' >&2; exit 2; }
if [[ "$saved_storage" == volume && "$Q4D_DATA_STORAGE" == bind ]]; then
  echo 'Automatic volume-to-bind conversion is not supported; keep volume storage or use a new state directory.' >&2
  exit 2
fi
if ((pull)); then
  ((build)) || { echo '--pull and --no-build cannot be combined.' >&2; exit 2; }
  build=0
  # Release bundles pin all four public images to the same version. Parse only
  # image names; never source a downloaded file as shell code.
  if [[ -f "$root/deploy/images.env" ]]; then
    while IFS='=' read -r key value; do
      case "$key" in Q4D_API_IMAGE|Q4D_WEB_IMAGE|Q4D_MCP_IMAGE|Q4D_AGENT_IMAGE) export "$key=$value" ;; esac
    done < "$root/deploy/images.env"
  fi
  if [[ -n "$image_prefix" ]]; then
    [[ "$image_prefix" =~ ^[a-z0-9][a-z0-9./_-]*$ && "$image_prefix" == */* && "$image_prefix" != */ ]] || { echo 'Invalid image prefix.' >&2; exit 2; }
    [[ "$image_tag" =~ ^[a-zA-Z0-9_][a-zA-Z0-9_.-]*$ && ${#image_tag} -le 128 ]] || { echo 'Invalid image tag.' >&2; exit 2; }
    export Q4D_API_IMAGE="$image_prefix-api:$image_tag" Q4D_WEB_IMAGE="$image_prefix-web:$image_tag"
    export Q4D_MCP_IMAGE="$image_prefix-mcp:$image_tag" Q4D_AGENT_IMAGE="$image_prefix-agent:$image_tag"
  elif [[ "$image_tag" != latest ]]; then
    echo '--image-tag requires --image-prefix.' >&2; exit 2
  fi
elif [[ -n "$image_prefix" || "$image_tag" != latest ]]; then
  echo '--image-prefix and --image-tag require --pull.' >&2; exit 2
fi
command -v docker >/dev/null || { echo 'Docker is required.' >&2; exit 1; }
docker compose version >/dev/null
docker info >/dev/null
export Q4D_STATE_DIR="$state"
export Q4D_UID="${Q4D_UID:-$(id -u)}" Q4D_GID="${Q4D_GID:-$(id -g)}"
if [[ "$Q4D_UID" == 0 ]]; then export Q4D_UID=65532 Q4D_GID=65532; fi
export Q4D_BIND="${Q4D_BIND:-127.0.0.1}" Q4D_WEB_PORT="${Q4D_WEB_PORT:-3000}" Q4D_MCP_PORT="${Q4D_MCP_PORT:-8090}"
export Q4D_PROJECT_NAME="${Q4D_PROJECT_NAME:-quant4dad-opensource}"
export Q4D_DATA_VOLUME="${Q4D_DATA_VOLUME:-$Q4D_PROJECT_NAME-data}"
if [[ "$Q4D_DATA_STORAGE" == volume ]]; then export Q4D_DATA_MOUNT=q4d_data; else export Q4D_DATA_MOUNT="$state/data"; fi
export Q4D_API_IMAGE="${Q4D_API_IMAGE:-quant4dad-opensource-api:local}" Q4D_WEB_IMAGE="${Q4D_WEB_IMAGE:-quant4dad-opensource-web:local}"
export Q4D_MCP_IMAGE="${Q4D_MCP_IMAGE:-quant4dad-opensource-mcp:local}" Q4D_AGENT_IMAGE="${Q4D_AGENT_IMAGE:-quant4dad-opensource-agent:local}"
export Q4D_SKIP_MIGRATION="${Q4D_SKIP_MIGRATION:-false}" Q4D_SKIP_SEED="${Q4D_SKIP_SEED:-false}" Q4D_NO_BACKGROUND="${Q4D_NO_BACKGROUND:-false}"
export Q4D_TIMEZONE="${Q4D_TIMEZONE:-Asia/Shanghai}"
[[ "$Q4D_TIMEZONE" =~ ^[A-Za-z0-9_+./-]+$ && "$Q4D_TIMEZONE" != *..* && "$Q4D_TIMEZONE" != /* ]] || { echo 'Invalid deployment timezone' >&2; exit 2; }
[[ "$Q4D_UID" =~ ^[1-9][0-9]*$ && "$Q4D_GID" =~ ^[0-9]+$ && "$Q4D_PROJECT_NAME" =~ ^[a-z0-9][a-z0-9_-]*$ ]] || exit 2
[[ "$Q4D_DATA_VOLUME" =~ ^[a-z0-9][a-z0-9_.-]*$ ]] || { echo 'Invalid data volume name.' >&2; exit 2; }
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
services=(api web)
((with_mcp==0)) || services+=(mcp)
((with_agent==0)) || services+=(agent)
if ((pull)); then
  for service in "${services[@]}"; do
    case "$service" in api) ref="$Q4D_API_IMAGE" ;; web) ref="$Q4D_WEB_IMAGE" ;; mcp) ref="$Q4D_MCP_IMAGE" ;; agent) ref="$Q4D_AGENT_IMAGE" ;; esac
    [[ "$ref" == */* && "$ref" != *[[:space:]]* ]] || { echo 'Use a release bundle or pass --image-prefix docker.io/OWNER/quant4dad-opensource.' >&2; exit 2; }
  done
fi
# This file contains only deployment settings. Business secrets stay in private YAML/token files.
deployment_candidate="$(mktemp "$state/deployment.env.XXXXXX")"
for key in Q4D_UID Q4D_GID Q4D_BIND Q4D_WEB_PORT Q4D_MCP_PORT Q4D_PROJECT_NAME Q4D_STATE_DIR Q4D_API_IMAGE Q4D_WEB_IMAGE Q4D_MCP_IMAGE Q4D_AGENT_IMAGE Q4D_SKIP_MIGRATION Q4D_SKIP_SEED Q4D_NO_BACKGROUND Q4D_TIMEZONE Q4D_DATA_STORAGE Q4D_DATA_VOLUME Q4D_DATA_MOUNT COMPOSE_PROFILES; do
 value="${!key}"; [[ "$value" != *$'\n'* && "$value" != *$'\r'* ]] || exit 2; printf '%s=%s\n' "$key" "$value" >> "$deployment_candidate"
done
compose=(docker compose --env-file "$deployment_candidate" -f "$root/deploy/compose.yaml")
if ((pull)); then
 "${compose[@]}" pull --policy always "${services[@]}"
fi
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
 setup+=(-e Q4D_SETUP_MYSQL_DSN_FILE=/setup/mysql-input)
fi
"${setup[@]}" "$Q4D_API_IMAGE" --setup /setup
rm -f "$state/mysql-input"

project_container_ids() {
  local project="$1" ids id source
  local containers=()
  ids="$(docker ps -aq --filter "label=com.docker.compose.project=$project")" || return 1
  while IFS= read -r id; do
    [[ -z "$id" ]] && continue
    source="$(docker inspect --format '{{range .Mounts}}{{if or (eq .Destination "/app/config/api.yaml") (eq .Destination "/app/config/web.yaml") (eq .Destination "/app/config/mcp.yaml") (eq .Destination "/run/q4d-config")}}{{.Source}}{{end}}{{end}}' "$id")" || return 1
    case "$source" in
      "$state/config/api.yaml"|"$state/config/web.yaml"|"$state/config/mcp.yaml"|"$state/agent/config") ;;
      *) echo 'A container with this project name belongs to a different state directory; refusing to modify it.' >&2; return 1 ;;
    esac
    containers+=("$id")
  done <<< "$ids"
  if ((${#containers[@]})); then printf '%s\n' "${containers[@]}"; fi
}
stop_previous_project() {
  local ids id
  local containers=()
  ids="$(project_container_ids "${saved_project:-$Q4D_PROJECT_NAME}")" || return 1
  while IFS= read -r id; do [[ -z "$id" ]] || containers+=("$id"); done <<< "$ids"
  if ((${#containers[@]})); then docker stop "${containers[@]}" >/dev/null; fi
}
project_container_ids "$Q4D_PROJECT_NAME" >/dev/null
stopped_previous=0
if [[ "$Q4D_DATA_STORAGE" == volume ]]; then
  # External volumes are never adopted by name alone. A changed state directory
  # or owner must not silently attach another installation's database.
  volume_labels='{{index .Labels "io.quant4dad.managed"}}|{{index .Labels "io.quant4dad.state-dir"}}|{{index .Labels "io.quant4dad.uid"}}|{{index .Labels "io.quant4dad.gid"}}'
  if ! docker volume inspect "$Q4D_DATA_VOLUME" >/dev/null 2>&1; then
    [[ "$saved_storage" != volume ]] || { echo 'The saved data volume is missing; refusing to start an empty database.' >&2; exit 1; }
    docker volume create --label io.quant4dad.managed=standalone-data-v1 --label "io.quant4dad.state-dir=$state" \
      --label "io.quant4dad.uid=$Q4D_UID" --label "io.quant4dad.gid=$Q4D_GID" "$Q4D_DATA_VOLUME" >/dev/null
  fi
  labels="$(docker volume inspect --format "$volume_labels" "$Q4D_DATA_VOLUME")"
  [[ "$labels" == "standalone-data-v1|$state|$Q4D_UID|$Q4D_GID" ]] || { echo 'Data volume belongs to another installation or owner; refusing to use it.' >&2; exit 1; }
  volume_state="$(docker run --rm --network none --user 0:0 --entrypoint sh \
    --mount "type=volume,source=$Q4D_DATA_VOLUME,target=/target,readonly,volume-nocopy" "$Q4D_API_IMAGE" -ec '
      entries=$(ls -A /target)
      if [ -f /target/.q4d-volume-ready ] && [ ! -L /target/.q4d-volume-ready ] && [ "$(cat /target/.q4d-volume-ready)" = standalone-data-v1 ]; then
        echo ready
      elif [ -z "$entries" ]; then
        echo empty
      else
        echo invalid
      fi' q4d-volume-inspect)"
  if [[ "$saved_storage" == volume ]]; then
    [[ "$volume_state" == ready ]] || { echo 'Saved data volume is not initialized; refusing to start an empty database.' >&2; exit 1; }
  else
    [[ "$volume_state" == empty ]] || { echo 'Target volume is not empty; refusing to overwrite it or reuse a possibly stale copy.' >&2; exit 1; }
    # Stop all containers of this installation before copying SQLite and its
    # companions. The original host directory remains an offline backup.
    stop_previous_project
    stopped_previous=1
    docker run --rm --network none --user 0:0 --entrypoint sh \
      --mount "type=volume,source=$Q4D_DATA_VOLUME,target=/target,volume-nocopy" \
      --mount "type=bind,source=$state/data,target=/source,readonly" "$Q4D_API_IMAGE" -ec '
        entries=$(ls -A /target)
        [ -z "$entries" ] || { echo "Target volume is not empty." >&2; exit 1; }
        cp -a /source/. /target/
        chown -hR "$1:$2" /target
        chmod 700 /target
        # Replace only our reserved marker; never follow a copied symlink.
        rm -f /target/.q4d-volume-ready
        printf "%s\n" standalone-data-v1 > /target/.q4d-volume-ready
        chmod 600 /target/.q4d-volume-ready
        chown "$1:$2" /target/.q4d-volume-ready
        sync' q4d-volume-initialize "$Q4D_UID" "$Q4D_GID"
  fi
fi
if [[ -n "$saved_project" && "$saved_project" != "$Q4D_PROJECT_NAME" && "$stopped_previous" == 0 ]]; then stop_previous_project; fi
# Save the selection only once storage is ready. Failed pull/setup/copy leaves
# the previous deployment settings intact and never starts a fresh empty store.
mv "$deployment_candidate" "$state/deployment.env"
deployment_candidate=""
compose=(docker compose --env-file "$state/deployment.env" -f "$root/deploy/compose.yaml")
"${compose[@]}" up -d --no-build --force-recreate --wait --wait-timeout 180
printf 'Web: http://%s:%s\nLogin token file: %s/config/login-token\n' "$Q4D_BIND" "$Q4D_WEB_PORT" "$state"
if ((with_mcp)); then printf 'MCP: http://%s:%s/mcp\nMCP token file: %s/config/mcp-token\n' "$Q4D_BIND" "$Q4D_MCP_PORT" "$state"; fi
printf 'Manage: docker compose --env-file %q -f %q ps\n' "$state/deployment.env" "$root/deploy/compose.yaml"
if [[ "$Q4D_DATA_STORAGE" == volume ]]; then printf 'Data volume: %s\n' "$Q4D_DATA_VOLUME"; fi
