#!/usr/bin/env bash
# Manages an isolated local Kubernetes lab (kind cluster 'commercelab-k8s-lab') for all 10 microservices,
# ephemeral infra stores, Prometheus metrics scraping, W3C traceparent verification, Chaos Game Day, and capacity testing.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

CLUSTER_NAME="commercelab-k8s-lab"
KIND_CONFIG="/tmp/${CLUSTER_NAME}-kind.yaml"
SECRETS_FILE="/tmp/${CLUSTER_NAME}-secrets.yaml"
PROM_IMAGE="prom/prometheus:v2.55.0"

GO_SERVICES=(
  api-gateway
  catalog-pricing-service
  inventory-warehouse-service
  order-checkout-service
  payment-service
  fulfillment-shipping-service
  ledger-reconciliation-service
  iam-auth-service
  notification-service
)
ALL_SERVICES=("${GO_SERVICES[@]}" backoffice-support-service)
INFRA_IMAGES=(
  postgres:16-alpine
  mongo:7.0
  redis:7.2-alpine
  docker.redpanda.com/redpandadata/redpanda:v24.1.4
)

CLEANUP_ON_FAIL="false"
CMD="${1:-}"
if [[ -z "${CMD}" ]]; then
  echo "Usage: $0 {up|verify|down} [--cleanup-on-fail]" >&2
  exit 2
fi
shift || true
while [[ $# -gt 0 ]]; do
  case "$1" in
    --cleanup-on-fail) CLEANUP_ON_FAIL="true"; shift ;;
    *) echo "Unknown flag: $1" >&2; exit 2 ;;
  esac
done

CLEANUP_ARMED="false"
on_exit() {
  local rc=$?
  trap - EXIT INT TERM
  rm -f "${KIND_CONFIG}" "${SECRETS_FILE}"
  if [[ "${CLEANUP_ARMED}" == "true" && "${rc}" != "0" ]]; then
    CLEANUP_ARMED="false"
    echo "[k8s-lab] Failure/interrupt detected; tearing down ${CLUSTER_NAME}..." >&2
    teardown || true
  fi
  exit "${rc}"
}

build_and_load_images() {
  echo "[k8s-lab] Building 10 service images sequentially..."
  for svc in "${GO_SERVICES[@]}"; do
    docker build -f "${BE_DIR}/Dockerfile" --build-arg "SERVICE_NAME=${svc}" -t "commercelab/${svc}:local" "${BE_DIR}"
  done
  docker build -f "${BE_DIR}/services/backoffice-support-service/Dockerfile" \
    -t "commercelab/backoffice-support-service:local" "${BE_DIR}/services/backoffice-support-service"

  echo "[k8s-lab] Loading 10 local app images into kind cluster ${CLUSTER_NAME}..."
  for svc in "${ALL_SERVICES[@]}"; do
    kind load docker-image --name "${CLUSTER_NAME}" "commercelab/${svc}:local"
  done
}

create_cluster() {
  if kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"; then
    echo "[k8s-lab] Deleting stale kind cluster ${CLUSTER_NAME}..."
    kind delete cluster --name "${CLUSTER_NAME}"
  fi
  cat > "${KIND_CONFIG}" <<'EOF'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    extraPortMappings:
      - containerPort: 30080
        hostPort: 38080
        listenAddress: "127.0.0.1"
      - containerPort: 30088
        hostPort: 38088
        listenAddress: "127.0.0.1"
      - containerPort: 30090
        hostPort: 39090
        listenAddress: "127.0.0.1"
      - containerPort: 30432
        hostPort: 35432
        listenAddress: "127.0.0.1"
      - containerPort: 30017
        hostPort: 37017
        listenAddress: "127.0.0.1"
EOF
  echo "[k8s-lab] Creating kind cluster ${CLUSTER_NAME}..."
  kind create cluster --name "${CLUSTER_NAME}" --config "${KIND_CONFIG}" --wait 120s
}

deploy_infra_and_migrate() {
  echo "[k8s-lab] Deploying commercelab-infra stores..."
  kubectl --context "kind-${CLUSTER_NAME}" create namespace commercelab-infra --dry-run=client -o yaml | kubectl --context "kind-${CLUSTER_NAME}" apply -f -
  kubectl --context "kind-${CLUSTER_NAME}" -n commercelab-infra create configmap commercelab-pg-init \
    --from-file=01-init-schemas.sql="${BE_DIR}/scripts/init-postgres-schemas.sql" \
    --dry-run=client -o yaml | kubectl --context "kind-${CLUSTER_NAME}" apply -f -
  kubectl --context "kind-${CLUSTER_NAME}" -n commercelab-infra create configmap commercelab-mongo-init \
    --from-file=01-init-indexes.js="${BE_DIR}/scripts/init-mongo-indexes.js" \
    --dry-run=client -o yaml | kubectl --context "kind-${CLUSTER_NAME}" apply -f -
  kubectl --context "kind-${CLUSTER_NAME}" apply -f "${BE_DIR}/k8s-local-kind/infra.yaml"

  for dep in postgres mongodb redis redpanda; do
    kubectl --context "kind-${CLUSTER_NAME}" -n commercelab-infra rollout status "deployment/${dep}" --timeout=180s
  done

  echo "[k8s-lab] Running SQL migrations and demo seed against kind NodePorts (35432 / 37017)..."
  export DATABASE_URL_IAM="postgres://iam_svc_user:iam_secret@127.0.0.1:35432/commerce_lab?sslmode=disable&search_path=iam"
  export DATABASE_URL_PRICING="postgres://pricing_svc_user:pricing_secret@127.0.0.1:35432/commerce_lab?sslmode=disable&search_path=pricing"
  export DATABASE_URL_INVENTORY="postgres://inventory_svc_user:inventory_secret@127.0.0.1:35432/commerce_lab?sslmode=disable&search_path=inventory"
  export DATABASE_URL_ORDERS="postgres://order_svc_user:order_secret@127.0.0.1:35432/commerce_lab?sslmode=disable&search_path=orders"
  export DATABASE_URL_PAYMENTS="postgres://payment_svc_user:payment_secret@127.0.0.1:35432/commerce_lab?sslmode=disable&search_path=payments"
  export DATABASE_URL_FULFILLMENT="postgres://fulfillment_svc_user:fulfillment_secret@127.0.0.1:35432/commerce_lab?sslmode=disable&search_path=fulfillment"
  export DATABASE_URL_LEDGER="postgres://ledger_svc_user:ledger_secret@127.0.0.1:35432/commerce_lab?sslmode=disable&search_path=ledger"
  export DATABASE_URL_SUPPORT="postgres://support_svc_user:support_secret@127.0.0.1:35432/commerce_lab?sslmode=disable&search_path=support"
  export DATABASE_URL_NOTIFICATIONS="postgres://notify_svc_user:notify_secret@127.0.0.1:35432/commerce_lab?sslmode=disable&search_path=notifications"
  export MONGO_URI="mongodb://mongo_admin:mongo_password@127.0.0.1:37017/commerce_lab_doc?authSource=admin&directConnection=true"
  export MONGO_DATABASE="commerce_lab_doc"

  PATH="/opt/homebrew/bin:${PATH}" GOTOOLCHAIN=local make -C "${BE_DIR}" migrate
  PATH="/opt/homebrew/bin:${PATH}" GOTOOLCHAIN=local make -C "${BE_DIR}" seed
}

deploy_services_and_monitoring() {
  echo "[k8s-lab] Creating ephemeral secrets and applying Kustomize local-kind overlay..."
  kubectl --context "kind-${CLUSTER_NAME}" apply -f "${BE_DIR}/k8s/namespace.yaml"
  cat > "${SECRETS_FILE}" <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: commercelab-secrets
  namespace: commercelab-retail
type: Opaque
stringData:
  JWT_ACCESS_SECRET: "$(openssl rand -hex 32)"
  JWT_REFRESH_SECRET: "$(openssl rand -hex 32)"
  INTERNAL_SERVICE_TOKEN: "$(openssl rand -hex 32)"
  PAYMENT_WEBHOOK_SECRET: "$(openssl rand -hex 32)"
  CARRIER_WEBHOOK_SECRET: "$(openssl rand -hex 32)"
  MONGO_URI: "mongodb://mongo_admin:mongo_password@mongodb.commercelab-infra.svc.cluster.local:27017/commerce_lab_doc?authSource=admin"
  DATABASE_URL_IAM: "postgres://iam_svc_user:iam_secret@postgres.commercelab-infra.svc.cluster.local:5432/commerce_lab?sslmode=disable&search_path=iam"
  DATABASE_URL_PRICING: "postgres://pricing_svc_user:pricing_secret@postgres.commercelab-infra.svc.cluster.local:5432/commerce_lab?sslmode=disable&search_path=pricing"
  DATABASE_URL_INVENTORY: "postgres://inventory_svc_user:inventory_secret@postgres.commercelab-infra.svc.cluster.local:5432/commerce_lab?sslmode=disable&search_path=inventory"
  DATABASE_URL_ORDERS: "postgres://order_svc_user:order_secret@postgres.commercelab-infra.svc.cluster.local:5432/commerce_lab?sslmode=disable&search_path=orders"
  DATABASE_URL_PAYMENTS: "postgres://payment_svc_user:payment_secret@postgres.commercelab-infra.svc.cluster.local:5432/commerce_lab?sslmode=disable&search_path=payments"
  DATABASE_URL_FULFILLMENT: "postgres://fulfillment_svc_user:fulfillment_secret@postgres.commercelab-infra.svc.cluster.local:5432/commerce_lab?sslmode=disable&search_path=fulfillment"
  DATABASE_URL_LEDGER: "postgres://ledger_svc_user:ledger_secret@postgres.commercelab-infra.svc.cluster.local:5432/commerce_lab?sslmode=disable&search_path=ledger"
  DATABASE_URL_SUPPORT: "postgres://support_svc_user:support_secret@postgres.commercelab-infra.svc.cluster.local:5432/commerce_lab?sslmode=disable&search_path=support"
  DATABASE_URL_NOTIFICATIONS: "postgres://notify_svc_user:notify_secret@postgres.commercelab-infra.svc.cluster.local:5432/commerce_lab?sslmode=disable&search_path=notifications"
EOF
  kubectl --context "kind-${CLUSTER_NAME}" apply -f "${SECRETS_FILE}"
  rm -f "${SECRETS_FILE}"

  kubectl --context "kind-${CLUSTER_NAME}" apply -k "${BE_DIR}/k8s-local-kind"
  kubectl --context "kind-${CLUSTER_NAME}" apply -f "${BE_DIR}/k8s-local-kind/prometheus.yaml"

  for svc in "${ALL_SERVICES[@]}"; do
    kubectl --context "kind-${CLUSTER_NAME}" -n commercelab-retail rollout status "deployment/${svc}" --timeout=180s
  done
  kubectl --context "kind-${CLUSTER_NAME}" -n monitoring rollout status deployment/prometheus --timeout=120s
}

verify_lab() {
  echo "[k8s-lab] Verifying pods, services, Prometheus targets, SLO metrics, and traceparent..."
  kubectl --context "kind-${CLUSTER_NAME}" -n commercelab-retail get pods,svc,networkpolicy,resourcequota,hpa

  local gw_health
  gw_health="$(curl -fsS -i -H "traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01" http://127.0.0.1:38080/health)"
  echo "${gw_health}" | grep -qi "traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-" || {
    echo "FAIL: W3C traceparent not propagated in response headers" >&2
    echo "${gw_health}" >&2
    return 1
  }
  echo "OK W3C traceparent propagated through api-gateway"

  # Wait up to 30s for Prometheus to scrape all 10 targets
  local up_count="0"
  for _ in $(seq 1 10); do
    up_count="$(curl -fsS "http://127.0.0.1:39090/api/v1/targets" | python3 -c '
import json, sys
d = json.load(sys.stdin)
targets = d.get("data", {}).get("activeTargets", [])
print(sum(1 for t in targets if t.get("health") == "up"))
')"
    if [[ "${up_count}" == "10" ]]; then
      break
    fi
    sleep 3
  done
  echo "Prometheus UP targets: ${up_count}/10"
  if [[ "${up_count}" != "10" ]]; then
    curl -fsS "http://127.0.0.1:39090/api/v1/targets" >&2
    return 1
  fi

  for metric in commercelab_service_up commercelab_stuck_pending_orders commercelab_unknown_payment_attempts commercelab_outbox_pending_rows commercelab_http_requests_total; do
    local res_count
    res_count="$(curl -fsS "http://127.0.0.1:39090/api/v1/query?query=${metric}" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["data"]["result"]))')"
    echo "Prometheus query ${metric}: ${res_count} series"
    if [[ "${res_count}" -lt 1 ]]; then
      echo "FAIL: missing metric ${metric} in Prometheus" >&2
      return 1
    fi
  done
}

teardown() {
  echo "[k8s-lab] Tearing down cluster ${CLUSTER_NAME} and ephemeral lab images..."
  kind delete cluster --name "${CLUSTER_NAME}" >/dev/null 2>&1 || true
  for svc in "${ALL_SERVICES[@]}"; do
    docker rmi -f "commercelab/${svc}:local" >/dev/null 2>&1 || true
  done
  local remaining_clusters remaining_imgs
  remaining_clusters="$(kind get clusters 2>/dev/null | grep -c "^${CLUSTER_NAME}$" || true)"
  remaining_imgs="$(docker images --format '{{.Repository}}:{{.Tag}}' | grep -c '^commercelab/.*:local$' || true)"
  echo "[k8s-lab] Cleanup check: clusters=${remaining_clusters}, local_app_images=${remaining_imgs}"
  if [[ "${remaining_clusters}" != "0" || "${remaining_imgs}" != "0" ]]; then
    return 1
  fi
}

trap on_exit EXIT INT TERM
case "${CMD}" in
  up)
    if [[ "${CLEANUP_ON_FAIL}" == "true" ]]; then
      CLEANUP_ARMED="true"
    fi
    create_cluster
    build_and_load_images
    deploy_infra_and_migrate
    deploy_services_and_monitoring
    CLEANUP_ARMED="false"
    ;;
  verify)
    verify_lab
    ;;
  down)
    teardown
    ;;
  *)
    echo "Usage: $0 {up|verify|down} [--cleanup-on-fail]" >&2
    exit 2
    ;;
esac
