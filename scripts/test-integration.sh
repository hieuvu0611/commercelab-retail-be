#!/usr/bin/env bash
# Runs the integration tests (go test -tags=integration) against real Postgres, Mongo and Redis started in a
# uniquely named, throw-away Compose project with temporary volumes. Everything is removed afterwards and the
# script fails if any container of the project is left behind.
set -euo pipefail

cd "$(dirname "$0")/.."

PROJECT="commerce-it-$$-${RANDOM}"
COMPOSE=(docker compose -p "$PROJECT")
STORES=(postgres mongodb redis)

free_port() {
  python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}

POSTGRES_PORT="$(free_port)"
MONGO_PORT="$(free_port)"
REDIS_PORT="$(free_port)"
export POSTGRES_HOST_PORT="$POSTGRES_PORT" MONGO_HOST_PORT="$MONGO_PORT" REDIS_HOST_PORT="$REDIS_PORT"

cleanup() {
  echo "Cleaning up Compose project ${PROJECT}..."
  "${COMPOSE[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
  leftovers="$(docker ps -aq --filter "label=com.docker.compose.project=${PROJECT}" | wc -l | tr -d ' ')"
  volumes="$(docker volume ls -q --filter "label=com.docker.compose.project=${PROJECT}" | wc -l | tr -d ' ')"
  echo "Leftover containers: ${leftovers}, volumes: ${volumes}"
  if [[ "${leftovers}" != "0" || "${volumes}" != "0" ]]; then
    exit 1
  fi
}
trap cleanup EXIT

echo "Starting ${STORES[*]} in project ${PROJECT}..."
"${COMPOSE[@]}" up -d --wait --wait-timeout 300 "${STORES[@]}"

# Each service connects as its own role to its own schema, exactly like in the application stack.
service_dsn() {
  echo "postgres://$1:$2@localhost:${POSTGRES_PORT}/commerce_lab?sslmode=disable&search_path=$3"
}
export DATABASE_URL_IAM="$(service_dsn iam_svc_user iam_secret iam)"
export DATABASE_URL_PRICING="$(service_dsn pricing_svc_user pricing_secret pricing)"
export DATABASE_URL_INVENTORY="$(service_dsn inventory_svc_user inventory_secret inventory)"
export DATABASE_URL_ORDERS="$(service_dsn order_svc_user order_secret orders)"
export DATABASE_URL_PAYMENTS="$(service_dsn payment_svc_user payment_secret payments)"
export DATABASE_URL_FULFILLMENT="$(service_dsn fulfillment_svc_user fulfillment_secret fulfillment)"
export DATABASE_URL_LEDGER="$(service_dsn ledger_svc_user ledger_secret ledger)"
export DATABASE_URL_SUPPORT="$(service_dsn support_svc_user support_secret support)"
export DATABASE_URL_NOTIFICATIONS="$(service_dsn notify_svc_user notify_secret notifications)"
export MONGO_URI="mongodb://mongo_admin:mongo_password@localhost:${MONGO_PORT}/commerce_lab_doc?authSource=admin"
export MONGO_DATABASE="commerce_lab_doc"
export REDIS_ADDR="localhost:${REDIS_PORT}"

echo "Applying migrations..."
make migrate

echo "Running integration tests..."
GO_ENV_PATH="$(GOTOOLCHAIN=local go env GOROOT)/bin"
PATH="${GO_ENV_PATH}:${PATH}" GOTOOLCHAIN=local go test -tags=integration -count=1 -v ./...
