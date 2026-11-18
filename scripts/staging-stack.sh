#!/usr/bin/env bash
# Manages an isolated, reproducible CommerceLab Retail staging stack using unique Compose projects,
# dynamically allocated loopback ports, ephemeral secrets, and project-scoped cleanup (AGENTS.md §3).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

usage() {
  cat <<'EOF'
Usage:
  staging-stack.sh up      --project <name> --state-dir <dir> [--be-dir <path>] [--cleanup-on-fail]
  staging-stack.sh smoke   --project <name> --state-dir <dir>
  staging-stack.sh redeploy --project <name> --state-dir <dir> --be-dir <path>
  staging-stack.sh backup  --project <name> --state-dir <dir> --backup-dir <dir>
  staging-stack.sh restore --project <name> --state-dir <dir> --backup-dir <dir> [--be-dir <path>] [--cleanup-on-fail]
  staging-stack.sh down    --project <name> --state-dir <dir> [--be-dir <path>]
EOF
}

free_port() {
  python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}

CMD="${1:-}"
if [[ -z "${CMD}" ]]; then
  usage
  exit 2
fi
shift

PROJECT=""
STATE_DIR=""
BACKUP_DIR=""
TARGET_BE_DIR="${BE_DIR}"
CLEANUP_ON_FAIL="false"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --project) PROJECT="$2"; shift 2 ;;
    --state-dir) STATE_DIR="$2"; shift 2 ;;
    --backup-dir) BACKUP_DIR="$2"; shift 2 ;;
    --be-dir) TARGET_BE_DIR="$2"; shift 2 ;;
    --cleanup-on-fail) CLEANUP_ON_FAIL="true"; shift ;;
    *) echo "Unknown argument: $1" >&2; usage; exit 2 ;;
  esac
done

if [[ -z "${PROJECT}" || -z "${STATE_DIR}" ]]; then
  echo "--project and --state-dir are required" >&2
  exit 2
fi

mkdir -p "${STATE_DIR}"
ENV_FILE="${STATE_DIR}/.env"
ENDPOINTS_FILE="${STATE_DIR}/endpoints.json"

APP_SERVICES=(
  api-gateway
  catalog-pricing-service
  inventory-warehouse-service
  order-checkout-service
  payment-service
  fulfillment-shipping-service
  ledger-reconciliation-service
  iam-auth-service
  backoffice-support-service
  notification-service
)
INFRA_SERVICES=(
  postgres
  mongodb
  redis
  object-storage
  redpanda
  opensearch
  clickhouse
)

init_env() {
  if [[ -f "${ENV_FILE}" ]]; then
    return
  fi
  local pg_port mongo_port redis_port s3_port rp_kafka_port rp_admin_port os_port ch_port gw_port bo_port noti_port
  pg_port="${POSTGRES_HOST_PORT:-$(free_port)}"
  mongo_port="${MONGO_HOST_PORT:-$(free_port)}"
  redis_port="${REDIS_HOST_PORT:-$(free_port)}"
  s3_port="${S3_HOST_PORT:-$(free_port)}"
  rp_kafka_port="${REDPANDA_KAFKA_HOST_PORT:-$(free_port)}"
  rp_admin_port="${REDPANDA_ADMIN_HOST_PORT:-$(free_port)}"
  os_port="${OPENSEARCH_HOST_PORT:-$(free_port)}"
  ch_port="${CLICKHOUSE_HTTP_PORT:-$(free_port)}"
  gw_port="${API_GATEWAY_HOST_PORT:-$(free_port)}"
  bo_port="${BACKOFFICE_SUPPORT_HOST_PORT:-$(free_port)}"
  noti_port="${NOTIFICATION_SERVICE_HOST_PORT:-$(free_port)}"

  cat > "${ENV_FILE}" <<EOF
APP_ENV=staging
PAYMENT_SIMULATOR_ENABLED=true
JWT_ACCESS_SECRET=$(openssl rand -hex 32)
JWT_REFRESH_SECRET=$(openssl rand -hex 32)
INTERNAL_SERVICE_TOKEN=$(openssl rand -hex 32)
PAYMENT_WEBHOOK_SECRET=$(openssl rand -hex 32)
CARRIER_WEBHOOK_SECRET=$(openssl rand -hex 32)
POSTGRES_HOST_PORT=${pg_port}
MONGO_HOST_PORT=${mongo_port}
REDIS_HOST_PORT=${redis_port}
S3_HOST_PORT=${s3_port}
REDPANDA_KAFKA_HOST_PORT=${rp_kafka_port}
REDPANDA_ADMIN_HOST_PORT=${rp_admin_port}
OPENSEARCH_HOST_PORT=${os_port}
CLICKHOUSE_HTTP_PORT=${ch_port}
API_GATEWAY_HOST_PORT=${gw_port}
BACKOFFICE_SUPPORT_HOST_PORT=${bo_port}
NOTIFICATION_SERVICE_HOST_PORT=${noti_port}
EOF
  chmod 600 "${ENV_FILE}"

  cat > "${ENDPOINTS_FILE}" <<EOF
{
  "project": "${PROJECT}",
  "postgresPort": ${pg_port},
  "mongoPort": ${mongo_port},
  "redisPort": ${redis_port},
  "s3Port": ${s3_port},
  "redpandaKafkaPort": ${rp_kafka_port},
  "redpandaAdminPort": ${rp_admin_port},
  "opensearchPort": ${os_port},
  "clickhousePort": ${ch_port},
  "gatewayUrl": "http://127.0.0.1:${gw_port}",
  "backofficeUrl": "http://127.0.0.1:${bo_port}",
  "notificationUrl": "http://127.0.0.1:${noti_port}",
  "opensearchUrl": "http://127.0.0.1:${os_port}",
  "clickhouseUrl": "http://127.0.0.1:${ch_port}"
}
EOF
}

load_env() {
  if [[ ! -f "${ENV_FILE}" ]]; then
    echo "Missing env file ${ENV_FILE}" >&2
    exit 1
  fi
  set -a
  # shellcheck disable=SC1090
  source "${ENV_FILE}"
  set +a
  export DATABASE_URL_IAM="postgres://iam_svc_user:iam_secret@127.0.0.1:${POSTGRES_HOST_PORT}/commerce_lab?sslmode=disable&search_path=iam"
  export DATABASE_URL_PRICING="postgres://pricing_svc_user:pricing_secret@127.0.0.1:${POSTGRES_HOST_PORT}/commerce_lab?sslmode=disable&search_path=pricing"
  export DATABASE_URL_INVENTORY="postgres://inventory_svc_user:inventory_secret@127.0.0.1:${POSTGRES_HOST_PORT}/commerce_lab?sslmode=disable&search_path=inventory"
  export DATABASE_URL_ORDERS="postgres://order_svc_user:order_secret@127.0.0.1:${POSTGRES_HOST_PORT}/commerce_lab?sslmode=disable&search_path=orders"
  export DATABASE_URL_PAYMENTS="postgres://payment_svc_user:payment_secret@127.0.0.1:${POSTGRES_HOST_PORT}/commerce_lab?sslmode=disable&search_path=payments"
  export DATABASE_URL_FULFILLMENT="postgres://fulfillment_svc_user:fulfillment_secret@127.0.0.1:${POSTGRES_HOST_PORT}/commerce_lab?sslmode=disable&search_path=fulfillment"
  export DATABASE_URL_LEDGER="postgres://ledger_svc_user:ledger_secret@127.0.0.1:${POSTGRES_HOST_PORT}/commerce_lab?sslmode=disable&search_path=ledger"
  export DATABASE_URL_SUPPORT="postgres://support_svc_user:support_secret@127.0.0.1:${POSTGRES_HOST_PORT}/commerce_lab?sslmode=disable&search_path=support"
  export DATABASE_URL_NOTIFICATIONS="postgres://notify_svc_user:notify_secret@127.0.0.1:${POSTGRES_HOST_PORT}/commerce_lab?sslmode=disable&search_path=notifications"
  export MONGO_URI="mongodb://mongo_admin:mongo_password@127.0.0.1:${MONGO_HOST_PORT}/commerce_lab_doc?authSource=admin"
  export MONGO_DATABASE="commerce_lab_doc"
}

compose_cmd() {
  local dir="$1"
  shift
  docker compose --project-directory "${dir}" -p "${PROJECT}" -f "${dir}/docker-compose.yml" --env-file "${ENV_FILE}" "$@"
}

# Build services sequentially so Go toolchains share GOCACHE without triggering BuildKit OOM on 8 GiB hosts.
build_app_services() {
  local dir="$1"
  for svc in "${APP_SERVICES[@]}"; do
    echo "Building ${svc} from ${dir}..."
    COMPOSE_PARALLEL_LIMIT=1 compose_cmd "${dir}" build "${svc}"
  done
}

cleanup_project() {
  echo "Cleaning up Compose project ${PROJECT}..."
  if [[ -f "${ENV_FILE}" ]]; then
    load_env || true
    compose_cmd "${TARGET_BE_DIR}" --profile app down --volumes --rmi local --remove-orphans || true
  else
    docker compose -p "${PROJECT}" -f "${TARGET_BE_DIR}/docker-compose.yml" --profile app down --volumes --rmi local --remove-orphans || true
  fi
  docker images --filter "reference=${PROJECT}-*" -q | sort -u | xargs docker rmi -f >/dev/null 2>&1 || true
  leftover_containers="$(docker ps -aq --filter "label=com.docker.compose.project=${PROJECT}" | wc -l | tr -d ' ')"
  leftover_volumes="$(docker volume ls -q --filter "label=com.docker.compose.project=${PROJECT}" | wc -l | tr -d ' ')"
  leftover_networks="$(docker network ls -q --filter "label=com.docker.compose.project=${PROJECT}" | wc -l | tr -d ' ')"
  leftover_images="$(docker images --filter "reference=${PROJECT}-*" -q | wc -l | tr -d ' ')"
  echo "Cleanup check (${PROJECT}): containers=${leftover_containers}, volumes=${leftover_volumes}, networks=${leftover_networks}, images=${leftover_images}"
  if [[ "${leftover_containers}" != "0" || "${leftover_volumes}" != "0" || "${leftover_networks}" != "0" || "${leftover_images}" != "0" ]]; then
    echo "Leftover resources detected for ${PROJECT}" >&2
    return 1
  fi
}

CLEANUP_ARMED="false"

on_exit_cleanup() {
  local exit_code=$?
  trap - EXIT INT TERM
  if [[ "${CLEANUP_ARMED}" == "true" ]]; then
    CLEANUP_ARMED="false"
    echo "Interrupted or failed during ${CMD} (exit=${exit_code}); running project cleanup for ${PROJECT}..." >&2
    cleanup_project || true
    if [[ "${exit_code}" == "0" ]]; then
      exit 1
    fi
  fi
  exit "${exit_code}"
}

enable_fail_cleanup_trap() {
  if [[ "${CLEANUP_ON_FAIL}" == "true" ]]; then
    CLEANUP_ARMED="true"
    trap on_exit_cleanup EXIT INT TERM
  fi
}

disable_fail_cleanup_trap() {
  CLEANUP_ARMED="false"
  trap - EXIT INT TERM
}

# Re-assert schema ownership and privileges for all 9 isolated service roles after pg_restore.
restore_pg_schema_permissions() {
  compose_cmd "${TARGET_BE_DIR}" exec -T postgres psql -U postgres -d commerce_lab -v ON_ERROR_STOP=1 <<'SQL'
DO $$
DECLARE
  pair record;
  tbl record;
  seq record;
BEGIN
  FOR pair IN
    SELECT * FROM (VALUES
      ('iam', 'iam_svc_user'),
      ('pricing', 'pricing_svc_user'),
      ('inventory', 'inventory_svc_user'),
      ('orders', 'order_svc_user'),
      ('payments', 'payment_svc_user'),
      ('fulfillment', 'fulfillment_svc_user'),
      ('ledger', 'ledger_svc_user'),
      ('support', 'support_svc_user'),
      ('notifications', 'notify_svc_user')
    ) AS t(schema_name, role_name)
  LOOP
    EXECUTE format('GRANT USAGE, CREATE ON SCHEMA %I TO %I', pair.schema_name, pair.role_name);
    EXECUTE format('GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA %I TO %I', pair.schema_name, pair.role_name);
    EXECUTE format('GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA %I TO %I', pair.schema_name, pair.role_name);
    FOR tbl IN SELECT tablename FROM pg_tables WHERE schemaname = pair.schema_name LOOP
      EXECUTE format('ALTER TABLE %I.%I OWNER TO %I', pair.schema_name, tbl.tablename, pair.role_name);
    END LOOP;
    FOR seq IN SELECT sequencename FROM pg_sequences WHERE schemaname = pair.schema_name LOOP
      EXECUTE format('ALTER SEQUENCE %I.%I OWNER TO %I', pair.schema_name, seq.sequencename, pair.role_name);
    END LOOP;
  END LOOP;
END
$$;
SQL
}

run_smoke() {
  load_env
  local checks=(
    "api-gateway:8080:.data.redisConnected == true"
    "iam-auth-service:8087:.data.dbConnected == true"
    "catalog-pricing-service:8081:.data.pgConnected == true and .data.mongoConnected == true"
    "inventory-warehouse-service:8082:.data.pgConnected == true"
    "order-checkout-service:8083:"
    "payment-service:8084:"
    "fulfillment-shipping-service:8085:"
    "ledger-reconciliation-service:8086:.data.pgConnected == true"
    "backoffice-support-service:8088:.data.postgresConnected == true"
    "notification-service:8089:"
  )
  local failures=0
  for check in "${checks[@]}"; do
    IFS=: read -r service port expr <<<"${check}"
    local body
    if ! body="$(compose_cmd "${TARGET_BE_DIR}" exec -T "${service}" wget -qO- "http://localhost:${port}/health" 2>&1)"; then
      echo "FAIL ${service}: /health unreachable: ${body}" >&2
      failures=$((failures + 1))
      continue
    fi
    if [[ -n "${expr}" ]] && ! jq -e "${expr}" <<<"${body}" >/dev/null; then
      echo "FAIL ${service}: expected '${expr}', got: ${body}" >&2
      failures=$((failures + 1))
      continue
    fi
    echo "OK   ${service}"
  done

  if ! curl -fsS "http://127.0.0.1:${OPENSEARCH_HOST_PORT}/_cluster/health?wait_for_status=yellow&timeout=5s" >/dev/null; then
    echo "FAIL opensearch health" >&2
    failures=$((failures + 1))
  else
    echo "OK   opensearch"
  fi
  if ! curl -fsS "http://127.0.0.1:${CLICKHOUSE_HTTP_PORT}/ping" | grep -q Ok; then
    echo "FAIL clickhouse health" >&2
    failures=$((failures + 1))
  else
    echo "OK   clickhouse"
  fi
  if ((failures > 0)); then
    echo "${failures} smoke check(s) failed" >&2
    exit 1
  fi
  cat "${ENDPOINTS_FILE}"
}

case "${CMD}" in
  up)
    init_env
    load_env
    enable_fail_cleanup_trap
    echo "Building all 10 microservices sequentially (pre-infra to bound peak host RAM)..."
    build_app_services "${TARGET_BE_DIR}"
    echo "Starting infrastructure for ${PROJECT}..."
    compose_cmd "${TARGET_BE_DIR}" up -d --wait --wait-timeout 300 "${INFRA_SERVICES[@]}"
    echo "Applying migrations across all 9 schemas..."
    make -C "${TARGET_BE_DIR}" migrate
    echo "Seeding demo catalog and inventory..."
    make -C "${TARGET_BE_DIR}" seed
    echo "Starting all 10 microservices..."
    compose_cmd "${TARGET_BE_DIR}" --profile app up -d --no-build --wait --wait-timeout 600 "${APP_SERVICES[@]}"
    run_smoke
    disable_fail_cleanup_trap
    ;;
  smoke)
    run_smoke
    ;;
  redeploy)
    load_env
    echo "Stopping application containers cleanly to free host proxy sockets and RAM..."
    compose_cmd "${TARGET_BE_DIR}" --profile app stop -t 2 "${APP_SERVICES[@]}"
    echo "Building and redeploying 10 application containers from ${TARGET_BE_DIR} into ${PROJECT}..."
    build_app_services "${TARGET_BE_DIR}"
    compose_cmd "${TARGET_BE_DIR}" --profile app up -d --no-build --no-deps --wait --wait-timeout 600 "${APP_SERVICES[@]}"
    run_smoke
    ;;
  backup)
    if [[ -z "${BACKUP_DIR}" ]]; then
      echo "--backup-dir is required for backup" >&2
      exit 2
    fi
    load_env
    mkdir -p "${BACKUP_DIR}"
    ts="$(date -u +%Y%m%dT%H%M%SZ)"
    echo "Backing up PostgreSQL, MongoDB, and S3 object-storage from ${PROJECT} to ${BACKUP_DIR}..."
    compose_cmd "${TARGET_BE_DIR}" exec -T postgres pg_dump -U postgres -d commerce_lab --format=custom > "${BACKUP_DIR}/postgres_commerce_lab.dump"
    compose_cmd "${TARGET_BE_DIR}" exec -T mongodb mongodump --username=mongo_admin --password=mongo_password --authenticationDatabase=admin --db=commerce_lab_doc --archive > "${BACKUP_DIR}/mongo_commerce_lab_doc.archive"
    s3_vol="$(docker volume ls -q --filter "label=com.docker.compose.project=${PROJECT}" --filter "label=com.docker.compose.volume=object_storage_data" | head -n1)"
    if [[ -n "${s3_vol}" ]]; then
      docker run --rm -v "${s3_vol}:/data:ro" postgres:16-alpine tar -czf - -C /data . > "${BACKUP_DIR}/object_storage_data.tar.gz"
    else
      echo "WARN: object_storage_data volume not found for ${PROJECT}" >&2
    fi
    cat > "${BACKUP_DIR}/manifest.json" <<EOF
{
  "project": "${PROJECT}",
  "capturedAtUtc": "${ts}",
  "postgresDump": "postgres_commerce_lab.dump",
  "mongoArchive": "mongo_commerce_lab_doc.archive",
  "objectStorageArchive": "object_storage_data.tar.gz",
  "authoritativeStores": ["postgres:commerce_lab", "mongodb:commerce_lab_doc", "object-storage:s3"],
  "rebuildableStores": ["redis", "opensearch", "clickhouse", "redpanda"]
}
EOF
    ls -lh "${BACKUP_DIR}"
    ;;
  restore)
    if [[ -z "${BACKUP_DIR}" ]]; then
      echo "--backup-dir is required for restore" >&2
      exit 2
    fi
    if [[ ! -f "${BACKUP_DIR}/postgres_commerce_lab.dump" || ! -f "${BACKUP_DIR}/mongo_commerce_lab_doc.archive" ]]; then
      echo "Backup files missing in ${BACKUP_DIR}" >&2
      exit 1
    fi
    init_env
    load_env
    enable_fail_cleanup_trap
    echo "Pre-building application images before starting restore infrastructure..."
    build_started_epoch="$(date +%s)"
    build_app_services "${TARGET_BE_DIR}"
    build_finished_epoch="$(date +%s)"
    started_epoch="$(date +%s)"
    echo "Starting fresh isolated stores for restore into ${PROJECT}..."
    compose_cmd "${TARGET_BE_DIR}" up -d --wait --wait-timeout 300 "${INFRA_SERVICES[@]}"
    echo "Restoring PostgreSQL dump and schema ownership..."
    compose_cmd "${TARGET_BE_DIR}" exec -T postgres pg_restore -U postgres -d commerce_lab --clean --if-exists < "${BACKUP_DIR}/postgres_commerce_lab.dump"
    restore_pg_schema_permissions
    echo "Restoring MongoDB archive..."
    compose_cmd "${TARGET_BE_DIR}" exec -T mongodb mongorestore --username=mongo_admin --password=mongo_password --authenticationDatabase=admin --drop --archive < "${BACKUP_DIR}/mongo_commerce_lab_doc.archive"
    if [[ -f "${BACKUP_DIR}/object_storage_data.tar.gz" ]]; then
      s3_vol="$(docker volume ls -q --filter "label=com.docker.compose.project=${PROJECT}" --filter "label=com.docker.compose.volume=object_storage_data" | head -n1)"
      if [[ -n "${s3_vol}" ]]; then
        echo "Restoring S3 object-storage volume (${s3_vol})..."
        compose_cmd "${TARGET_BE_DIR}" stop -t 2 object-storage
        docker run --rm -i -v "${s3_vol}:/data" postgres:16-alpine sh -c 'rm -rf /data/* && tar -xzf - -C /data' < "${BACKUP_DIR}/object_storage_data.tar.gz"
        compose_cmd "${TARGET_BE_DIR}" up -d --wait --wait-timeout 120 object-storage
      fi
    fi
    echo "Starting application services against restored data..."
    compose_cmd "${TARGET_BE_DIR}" --profile app up -d --no-build --wait --wait-timeout 600 "${APP_SERVICES[@]}"
    finished_epoch="$(date +%s)"
    build_seconds="$((build_finished_epoch - build_started_epoch))"
    rto_seconds="$((finished_epoch - started_epoch))"
    total_dr_seconds="$((finished_epoch - build_started_epoch))"
    echo "Build duration: ${build_seconds}s | Restore RTO (infra up + restore + app healthy): ${rto_seconds}s | Cold-build+Restore total: ${total_dr_seconds}s"
    run_smoke
    disable_fail_cleanup_trap
    ;;
  down)
    cleanup_project
    ;;
  *)
    usage
    exit 2
    ;;
esac
