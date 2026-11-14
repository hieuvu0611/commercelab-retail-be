#!/usr/bin/env bash
# Starts the full app stack in a uniquely named, throw-away Compose project, checks every /health
# endpoint (including store connectivity where a service reports it) and removes everything afterwards.
set -euo pipefail

cd "$(dirname "$0")/.."

PROJECT="commerce-smoke-$$-${RANDOM}"
COMPOSE=(docker compose -p "$PROJECT" --profile app)
failures=0

free_port() {
  python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}

# The services refuse to start without JWT secrets; these exist only for this throw-away project.
JWT_ACCESS_SECRET="$(openssl rand -hex 32)"
JWT_REFRESH_SECRET="$(openssl rand -hex 32)"
export JWT_ACCESS_SECRET JWT_REFRESH_SECRET

# Use free host ports so the smoke run never collides with a stack that is already running.
export POSTGRES_HOST_PORT="$(free_port)" MONGO_HOST_PORT="$(free_port)" REDIS_HOST_PORT="$(free_port)"
export REDPANDA_KAFKA_HOST_PORT="$(free_port)" REDPANDA_ADMIN_HOST_PORT="$(free_port)"
export API_GATEWAY_HOST_PORT="$(free_port)" BACKOFFICE_SUPPORT_HOST_PORT="$(free_port)" NOTIFICATION_SERVICE_HOST_PORT="$(free_port)"

cleanup() {
  echo "Cleaning up Compose project ${PROJECT}..."
  # Only containers, networks, volumes and images created by this project are removed.
  "${COMPOSE[@]}" down --volumes --rmi local --remove-orphans >/dev/null 2>&1 || true
  # Built images are tagged "<project>-<service>", which "--rmi local" leaves behind.
  docker images --filter "reference=${PROJECT}-*" -q | sort -u | xargs docker rmi -f >/dev/null 2>&1 || true
  leftovers="$(docker ps -aq --filter "label=com.docker.compose.project=${PROJECT}" | wc -l | tr -d ' ')"
  images="$(docker images --filter "reference=${PROJECT}-*" -q | wc -l | tr -d ' ')"
  echo "Leftover containers: ${leftovers}, images: ${images}"
}
trap cleanup EXIT

# service name : container port : jq expression that must be true (empty = HTTP 200 only)
CHECKS=(
  "api-gateway:8080:.data.redisConnected == true"
  "iam-auth-service:8087:.data.dbConnected == true"
  "catalog-pricing-service:8081:.data.pgConnected == true and .data.mongoConnected == true"
  "inventory-warehouse-service:8082:.data.pgConnected == true"
  "order-checkout-service:8083:"
  "payment-service:8084:"
  "fulfillment-shipping-service:8085:"
  "ledger-reconciliation-service:8086:"
  "backoffice-support-service:8088:"
  "notification-service:8089:"
)

echo "Starting project ${PROJECT} (this builds all images)..."
"${COMPOSE[@]}" up -d --build --wait --wait-timeout 600

for check in "${CHECKS[@]}"; do
  IFS=: read -r service port expression <<<"${check}"
  if ! body="$("${COMPOSE[@]}" exec -T "${service}" wget -qO- "http://localhost:${port}/health" 2>&1)"; then
    echo "FAIL ${service}: /health unreachable: ${body}"
    failures=$((failures + 1))
    continue
  fi
  if [[ -n "${expression}" ]] && ! jq -e "${expression}" <<<"${body}" >/dev/null; then
    echo "FAIL ${service}: expected '${expression}', got: ${body}"
    failures=$((failures + 1))
    continue
  fi
  echo "OK   ${service}"
done

if ((failures > 0)); then
  echo "${failures} health check(s) failed"
  exit 1
fi
echo "All health checks passed"
