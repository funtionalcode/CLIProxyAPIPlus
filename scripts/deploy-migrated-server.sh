#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP_DIR="${APP_DIR:-$(cd "${SCRIPT_DIR}/.." && pwd)}"
WEB_DIST="${WEB_DIST:-${APP_DIR}/../Cli-Proxy-API-Management-Center/dist}"
API_PORT="${API_PORT:-8317}"
MANAGER_PORT="${MANAGER_PORT:-18317}"
BUILD_IMAGE="${BUILD_IMAGE:-0}"
PULL_REPO="${PULL_REPO:-0}"

cd "${APP_DIR}"
mkdir -p auths plugins logs cpa-manager-data

ENV_FILE_ARGS=()
if [[ -f .env ]]; then
  set -a
  set +u
  # shellcheck disable=SC1091
  source .env
  set -u
  set +a
  ENV_FILE_ARGS=(--env-file "${APP_DIR}/.env")
fi

API_IMAGE="${CLI_PROXY_IMAGE:-eceasy/cli-proxy-api:latest}"
MANAGER_IMAGE="${CPA_MANAGER_IMAGE:-seakee/cpa-manager:latest}"

if [[ "${PULL_REPO}" == "1" ]]; then
  echo "[deploy] updating repository"
  git fetch --all --prune
  git pull --ff-only
fi

compose() {
  if docker compose version >/dev/null 2>&1; then
    docker compose "$@"
    return
  fi
  if command -v docker-compose >/dev/null 2>&1; then
    docker-compose "$@"
    return
  fi
  return 127
}

detect_public_host() {
  if [[ -n "${PUBLIC_HOST:-}" ]]; then
    printf '%s\n' "${PUBLIC_HOST}"
    return
  fi
  ip route get 1.1.1.1 2>/dev/null | awk '
    {
      for (i = 1; i <= NF; i++) {
        if ($i == "src") {
          print $(i + 1)
          exit
        }
      }
    }
  ' | head -n1
}

if [[ "${BUILD_IMAGE}" == "1" ]]; then
  echo "[deploy] building ${API_IMAGE}"
  VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
  COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo none)}"
  BUILD_DATE="${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
  docker build \
    --network host \
    --build-arg HTTP_PROXY="${HTTP_PROXY:-}" \
    --build-arg HTTPS_PROXY="${HTTPS_PROXY:-${HTTP_PROXY:-}}" \
    --build-arg ALL_PROXY="${ALL_PROXY:-${HTTP_PROXY:-}}" \
    --build-arg NO_PROXY="${NO_PROXY:-localhost,127.0.0.1,host.docker.internal}" \
    --build-arg GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}" \
    --build-arg GOSUMDB="${GOSUMDB:-sum.golang.org}" \
    --build-arg VERSION="${VERSION}" \
    --build-arg COMMIT="${COMMIT}" \
    --build-arg BUILD_DATE="${BUILD_DATE}" \
    -t "${API_IMAGE}" \
    .
fi

if ! docker image inspect "${API_IMAGE}" >/dev/null 2>&1; then
  echo "missing API image: ${API_IMAGE}" >&2
  echo "Import the migrated image first, or rerun with BUILD_IMAGE=1." >&2
  exit 1
fi

if ! docker image inspect "${MANAGER_IMAGE}" >/dev/null 2>&1; then
  echo "missing manager image: ${MANAGER_IMAGE}" >&2
  exit 1
fi

echo "[deploy] starting cpa-manager on ${MANAGER_PORT}"
if compose up -d --no-build cpa-manager; then
  :
else
  echo "[deploy] compose manager start failed, falling back to docker run"
  docker rm -f cpa-manager >/dev/null 2>&1 || true
  docker run -d \
    --name cpa-manager \
    --restart unless-stopped \
    -p "${MANAGER_PORT}:18317" \
    -e HTTP_ADDR="0.0.0.0:18317" \
    -e USAGE_DB_PATH="/data/usage.sqlite" \
    -e USAGE_COLLECTOR_MODE="auto" \
    -e USAGE_RESP_QUEUE="usage" \
    -e USAGE_RESP_POP_SIDE="right" \
    -e USAGE_BATCH_SIZE="100" \
    -e USAGE_POLL_INTERVAL_MS="500" \
    -e USAGE_QUERY_LIMIT="50000" \
    -e USAGE_CORS_ORIGINS="*" \
    -v "${APP_DIR}/cpa-manager-data:/data" \
    "${MANAGER_IMAGE}" >/dev/null
fi

echo "[deploy] starting cli-proxy-api on host port ${API_PORT}"
docker rm -f cli-proxy-api >/dev/null 2>&1 || true
docker run -d \
  --name cli-proxy-api \
  --restart unless-stopped \
  --network host \
  "${ENV_FILE_ARGS[@]}" \
  -e DEPLOY="${DEPLOY:-}" \
  -v "${CLI_PROXY_CONFIG_PATH:-${APP_DIR}/config.yaml}:/CLIProxyAPI/config.yaml" \
  -v "${CLI_PROXY_AUTH_PATH:-${APP_DIR}/auths}:/root/.cli-proxy-api" \
  -v "${CLI_PROXY_PLUGIN_PATH:-${APP_DIR}/plugins}:/CLIProxyAPI/plugins" \
  -v "${CLI_PROXY_LOG_PATH:-${APP_DIR}/logs}:/CLIProxyAPI/logs" \
  -v "${CLI_PROXY_STATIC_PATH:-${WEB_DIST}}:/CLIProxyAPI/static" \
  "${API_IMAGE}" >/dev/null

echo "[deploy] containers"
docker ps --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}' \
  | grep -E '^(NAMES|cli-proxy-api|cpa-manager)'

echo "[deploy] local checks"
for i in $(seq 1 20); do
  if curl -sS --max-time 3 "http://127.0.0.1:${MANAGER_PORT}/health" >/tmp/cpa-manager-health.out; then
    if grep -q '"ok":true' /tmp/cpa-manager-health.out; then
      break
    fi
  fi
  if [[ "${i}" == "20" ]]; then
    echo "cpa-manager health check timed out" >&2
    cat /tmp/cpa-manager-health.out >&2 || true
    exit 1
  fi
  sleep 1
done

for i in $(seq 1 30); do
  curl -sS --max-time 3 "http://127.0.0.1:${API_PORT}/v1/models" \
    >/tmp/cli-proxy-api-models.out 2>/tmp/cli-proxy-api-models.err || true
  if grep -q "Missing API key" /tmp/cli-proxy-api-models.out; then
    break
  fi
  if [[ "${i}" == "30" ]]; then
    echo "cli-proxy-api did not return the expected auth challenge" >&2
    cat /tmp/cli-proxy-api-models.err >&2 || true
    cat /tmp/cli-proxy-api-models.out >&2 || true
    exit 1
  fi
  sleep 1
done

PUBLIC_ACCESS_HOST="$(detect_public_host)"
if [[ -n "${PUBLIC_ACCESS_HOST}" ]]; then
  echo "[deploy] access"
  echo "CLIProxyAPI: http://${PUBLIC_ACCESS_HOST}:${API_PORT}"
  echo "CPA manager: http://${PUBLIC_ACCESS_HOST}:${MANAGER_PORT}"
  echo "CPA manager health: http://${PUBLIC_ACCESS_HOST}:${MANAGER_PORT}/health"
fi

echo "[deploy] ok"
