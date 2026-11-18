"""Mixed staging load benchmark covering catalog reads, cart/quote checks, and order creation."""

from __future__ import annotations

import argparse
import concurrent.futures
import json
import sys
import threading
import time
from typing import Any

from bench_common import (
    DockerStatsSampler,
    KeepAliveClient,
    inspect_project_health,
    summarize_latencies,
)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Run mixed read/order staging workload.")
    parser.add_argument("--url", required=True, help="API gateway base URL (e.g. http://127.0.0.1:18080)")
    parser.add_argument("--email", default="lan@example.com", help="Seeded customer email for fallback single-user runs")
    parser.add_argument("--password", default="Dev-Passw0rd-2026", help="Customer password")
    parser.add_argument("--register-workers", action="store_true", help="Register dedicated test customers per worker")
    parser.add_argument("--sku", default="SKU-BOTTLE-BLUE-500ML", help="In-stock SKU for cart/quote/order checks")
    parser.add_argument("--duration-seconds", type=int, default=20, help="Measured run duration in seconds")
    parser.add_argument("--warmup-seconds", type=int, default=3, help="Warmup duration in seconds")
    parser.add_argument("--concurrency", type=int, default=6, help="Number of concurrent workers")
    parser.add_argument("--docker-project", default="", help="Optional Compose project name for resource sampling")
    parser.add_argument("--output", default="", help="Optional file path to save raw JSON report")
    return parser.parse_args()


def prepare_worker_session(client: KeepAliveClient, args: argparse.Namespace, worker_idx: int) -> dict[str, str]:
    """Authenticate a customer session and ensure one default shipping address exists."""
    corr = f"load-setup-w{worker_idx}-{int(time.time() * 1000)}"
    if args.register_workers:
        email = f"load.worker.{worker_idx}.{int(time.time())}@example.com"
        status, parsed, raw, _ = client.request(
            "POST",
            "/api/v1/auth/register",
            headers={"X-Correlation-ID": corr},
            body={"email": email, "password": args.password, "fullName": f"Load Worker {worker_idx}"},
        )
        if status not in (200, 201) or not parsed or not parsed.get("data", {}).get("accessToken"):
            raise RuntimeError(f"Register failed for worker {worker_idx}: status={status} body={raw[:200]}")
        token = str(parsed["data"]["accessToken"])
    else:
        status, parsed, raw, _ = client.request(
            "POST",
            "/api/v1/auth/login",
            headers={"X-Correlation-ID": corr},
            body={"email": args.email, "password": args.password},
        )
        if status != 200 or not parsed or not parsed.get("data", {}).get("accessToken"):
            raise RuntimeError(f"Login failed for {args.email}: status={status} body={raw[:200]}")
        token = str(parsed["data"]["accessToken"])

    auth_hdr = {"Authorization": f"Bearer {token}", "X-Correlation-ID": f"{corr}-addr"}
    status, parsed, raw, _ = client.request("GET", "/api/v1/users/me/addresses", headers=auth_hdr)
    if status != 200 or not parsed:
        raise RuntimeError(f"List addresses failed: status={status} body={raw[:200]}")
    addresses = parsed.get("data") or []
    if addresses:
        address_id = str(addresses[0]["id"])
    else:
        status, parsed, raw, _ = client.request(
            "POST",
            "/api/v1/users/me/addresses",
            headers=auth_hdr,
            body={
                "recipient": f"Load Tester {worker_idx}",
                "phone": "0901234567",
                "line1": "12 Nguyen Hue",
                "ward": "Ben Nghe",
                "district": "District 1",
                "province": "Ho Chi Minh City",
                "isDefault": True,
            },
        )
        if status not in (200, 201) or not parsed or not parsed.get("data", {}).get("id"):
            raise RuntimeError(f"Create address failed: status={status} body={raw[:200]}")
        address_id = str(parsed["data"]["id"])

    return {"token": token, "addressId": address_id}


def choose_operation(step: int) -> str:
    """Deterministic 70% read / 20% cart-promo / 10% create-order mix."""
    slot = step % 10
    if slot < 7:
        return "read_catalog_stock"
    if slot < 9:
        return "read_cart_promotions"
    return "create_order"


def run_phase(
    seconds: int,
    args: argparse.Namespace,
    sessions: list[dict[str, str]],
    prefix: str,
) -> dict[str, Any]:
    """Execute the mixed workload for `seconds` across worker sessions."""
    lock = threading.Lock()
    op_latencies: dict[str, list[float]] = {
        "read_catalog_stock": [],
        "read_cart_promotions": [],
        "create_order": [],
    }
    all_latencies: list[float] = []
    op_counts: dict[str, dict[str, int]] = {
        k: {"ok": 0, "businessRejected": 0, "infraError": 0} for k in op_latencies
    }
    created_orders: list[str] = []
    sample_correlations: list[dict[str, Any]] = []
    infra_errors: list[str] = []

    deadline = time.perf_counter() + seconds

    def record(op: str, status: int, corr_id: str, latency_ms: float, category: str, detail: str = "") -> None:
        with lock:
            op_counts[op][category] += 1
            if category == "ok":
                op_latencies[op].append(latency_ms)
                all_latencies.append(latency_ms)
            if len(sample_correlations) < 12:
                sample_correlations.append({"op": op, "status": status, "correlationId": corr_id, "outcome": category})
            if category == "infraError" and len(infra_errors) < 20:
                infra_errors.append(f"{op} {corr_id}: status={status} {detail[:180]}")

    def worker(worker_idx: int) -> None:
        session = sessions[worker_idx]
        auth_headers = {"Authorization": f"Bearer {session['token']}"}
        client = KeepAliveClient(args.url)
        step = 0
        try:
            while time.perf_counter() < deadline:
                op = choose_operation(step)
                corr_id = f"{prefix}-w{worker_idx}-s{step}"
                step += 1

                if op == "read_catalog_stock":
                    if step % 2 == 0:
                        path = "/api/v1/products?q=backpack&limit=10"
                    else:
                        path = f"/api/v1/inventory/availability?skuIds={args.sku}&warehouse=ALL"
                    status, _, raw, lat = client.request("GET", path, headers={"X-Correlation-ID": corr_id})
                    if status == 200:
                        record(op, status, corr_id, lat, "ok")
                    else:
                        record(op, status, corr_id, lat, "infraError", raw)
                elif op == "read_cart_promotions":
                    path = "/api/v1/carts/me" if step % 2 == 0 else "/api/v1/promotions/active"
                    status, _, raw, lat = client.request(
                        "GET", path, headers={**auth_headers, "X-Correlation-ID": corr_id}
                    )
                    if status == 200:
                        record(op, status, corr_id, lat, "ok")
                    else:
                        record(op, status, corr_id, lat, "infraError", raw)
                else:
                    started = time.perf_counter()
                    # 1) Put item in cart
                    s1, _, r1, _ = client.request(
                        "PUT",
                        f"/api/v1/carts/me/items/{args.sku}",
                        headers={**auth_headers, "X-Correlation-ID": f"{corr_id}-cart"},
                        body={"quantity": 1},
                    )
                    if s1 != 200:
                        lat = (time.perf_counter() - started) * 1000.0
                        record(op, s1, f"{corr_id}-cart", lat, "businessRejected" if 400 <= s1 < 500 else "infraError", r1)
                        continue
                    # 2) Create quote
                    s2, p2, r2, _ = client.request(
                        "POST",
                        "/api/v1/quotes",
                        headers={**auth_headers, "X-Correlation-ID": f"{corr_id}-quote"},
                        body={"addressId": session["addressId"]},
                    )
                    quote_id = str((p2 or {}).get("data", {}).get("quoteId", ""))
                    if s2 not in (200, 201) or not quote_id:
                        lat = (time.perf_counter() - started) * 1000.0
                        record(op, s2, f"{corr_id}-quote", lat, "businessRejected" if 400 <= s2 < 500 else "infraError", r2)
                        continue
                    # 3) Place order with unique idempotency key
                    idem_key = f"{prefix}-ord-{worker_idx}-{step}-{int(time.time() * 1000)}"
                    s3, p3, r3, _ = client.request(
                        "POST",
                        "/api/v1/orders",
                        headers={
                            **auth_headers,
                            "X-Correlation-ID": corr_id,
                            "Idempotency-Key": idem_key,
                        },
                        body={"quoteId": quote_id, "paymentMethod": "PREPAID"},
                    )
                    lat = (time.perf_counter() - started) * 1000.0
                    if s3 in (200, 201):
                        order_id = str((p3 or {}).get("data", {}).get("orderId", ""))
                        with lock:
                            if order_id and len(created_orders) < 50:
                                created_orders.append(order_id)
                        record(op, s3, corr_id, lat, "ok")
                    elif s3 in (409, 422):
                        record(op, s3, corr_id, lat, "businessRejected", r3)
                    else:
                        record(op, s3, corr_id, lat, "infraError", r3)
        finally:
            client.close()

    started = time.perf_counter()
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.concurrency) as pool:
        futures = [pool.submit(worker, idx) for idx in range(args.concurrency)]
        for fut in futures:
            fut.result()
    elapsed_s = max(time.perf_counter() - started, 1e-6)

    total_reqs = sum(sum(v.values()) for v in op_counts.values())
    total_ok = sum(v["ok"] for v in op_counts.values())
    total_biz = sum(v["businessRejected"] for v in op_counts.values())
    total_infra = sum(v["infraError"] for v in op_counts.values())

    return {
        "durationSeconds": round(elapsed_s, 4),
        "totalRequests": total_reqs,
        "succeeded": total_ok,
        "businessRejected": total_biz,
        "infraErrors": total_infra,
        "infraErrorRatePct": round((total_infra / total_reqs) * 100.0, 4) if total_reqs else 0.0,
        "throughputRps": round(total_reqs / elapsed_s, 2),
        "overallLatency": summarize_latencies(all_latencies),
        "byOperation": {
            op: {**op_counts[op], "latency": summarize_latencies(op_latencies[op])} for op in op_latencies
        },
        "sampleOrderIds": created_orders[:10],
        "sampleCorrelations": sample_correlations,
        "sampleInfraErrors": infra_errors,
    }


def main() -> int:
    args = parse_args()
    sessions: list[dict[str, str]] = []
    setup_client = KeepAliveClient(args.url)
    try:
        if args.register_workers:
            for idx in range(args.concurrency):
                sessions.append(prepare_worker_session(setup_client, args, idx))
        else:
            base_session = prepare_worker_session(setup_client, args, 0)
            sessions = [base_session for _ in range(args.concurrency)]
    finally:
        setup_client.close()

    warmup = run_phase(args.warmup_seconds, args, sessions, f"load-warm-{int(time.time())}")
    sampler = DockerStatsSampler(args.docker_project, interval_s=1.0)
    sampler.start()
    measured = run_phase(args.duration_seconds, args, sessions, f"load-run-{int(time.time())}")
    resources = sampler.stop()
    container_health = inspect_project_health(args.docker_project)

    report = {
        "benchmark": "staging-mixed-read-order",
        "targetUrl": args.url,
        "sku": args.sku,
        "concurrency": args.concurrency,
        "requestMix": "70% catalog/inventory reads, 20% cart/promotions reads, 10% quote+order create",
        "warmup": warmup,
        "measured": measured,
        "resources": resources,
        "containerHealth": container_health,
    }
    out = json.dumps(report, indent=2)
    if args.output:
        with open(args.output, "w", encoding="utf-8") as fh:
            fh.write(out + "\n")
    print(out)
    has_oom_or_restart = any(row["restarts"] > 0 or row["oomKilled"] for row in container_health)
    return 0 if measured["infraErrors"] == 0 and not has_oom_or_restart else 1


if __name__ == "__main__":
    sys.exit(main())
