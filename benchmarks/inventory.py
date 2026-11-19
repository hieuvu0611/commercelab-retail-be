"""Repeatable read-only inventory benchmark matching docs/10-low-resource-benchmark.md."""

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
    parser = argparse.ArgumentParser(description="Run closed-loop inventory availability benchmark.")
    parser.add_argument("--url", required=True, help="Gateway or inventory base URL (e.g. http://127.0.0.1:18080)")
    parser.add_argument("--sku", default="SKU-BALO-BLACK-20L", help="Seeded SKU ID to query and validate")
    parser.add_argument("--warehouse", default="ALL", help="Warehouse filter (default: ALL)")
    parser.add_argument("--expected-state", default="IN_STOCK", help="Expected availability state on public endpoint (IN_STOCK/LOW_STOCK/OUT_OF_STOCK)")
    parser.add_argument("--staff-token", default="", help="Optional staff JWT to query /api/v1/inventory/{sku}")
    parser.add_argument("--expected-available", type=int, default=-1, help="Expected available count when --staff-token is set")
    parser.add_argument("--warmup", type=int, default=10, help="Warmup requests before measurement")
    parser.add_argument("--requests", type=int, default=100, help="Measured requests")
    parser.add_argument("--concurrency", type=int, default=10, help="Concurrent closed-loop workers")
    parser.add_argument("--docker-project", default="", help="Optional Compose project name for resource sampling")
    parser.add_argument("--output", default="", help="Optional path to write raw JSON report")
    return parser.parse_args()


def validate_response(
    status: int,
    parsed: dict[str, Any] | None,
    raw: str,
    args: argparse.Namespace,
) -> tuple[bool, str]:
    """Verify HTTP 200 and live database-backed inventory content."""
    if status != 200 or not parsed:
        return False, f"http={status} body={raw[:200]}"
    data = parsed.get("data")
    if not isinstance(data, dict):
        return False, "missing data object"

    if args.staff_token:
        available = data.get("available")
        if args.expected_available >= 0 and available != args.expected_available:
            return False, f"available={available}, expected={args.expected_available}"
        if not isinstance(available, int):
            return False, f"unexpected staff inventory payload: {data}"
        return True, ""

    items = data.get("items")
    if not isinstance(items, list):
        return False, "missing availability items"
    for item in items:
        if isinstance(item, dict) and item.get("skuId") == args.sku:
            state = item.get("state")
            if args.expected_state and state != args.expected_state:
                return False, f"state={state}, expected={args.expected_state}"
            return True, ""
    return False, f"SKU {args.sku} missing from availability response"


def run_batch(
    total: int,
    concurrency: int,
    args: argparse.Namespace,
    prefix: str,
) -> dict[str, Any]:
    """Run `total` closed-loop requests across `concurrency` keep-alive workers."""
    counter_lock = threading.Lock()
    issued = 0
    latencies: list[float] = []
    errors: list[str] = []
    corr_ids: list[str] = []

    if args.staff_token:
        path = f"/api/v1/inventory/{args.sku}?warehouseId={args.warehouse}"
        base_headers = {"Authorization": f"Bearer {args.staff_token}"}
    else:
        path = f"/api/v1/inventory/availability?skuIds={args.sku}&warehouse={args.warehouse}"
        base_headers = {}

    def worker(worker_idx: int) -> None:
        nonlocal issued
        client = KeepAliveClient(args.url)
        try:
            while True:
                with counter_lock:
                    if issued >= total:
                        return
                    seq = issued
                    issued += 1
                corr_id = f"{prefix}-w{worker_idx}-{seq}"
                headers = {**base_headers, "X-Correlation-ID": corr_id}
                status, parsed, raw, latency_ms = client.request("GET", path, headers=headers)
                ok, reason = validate_response(status, parsed, raw, args)
                with counter_lock:
                    if len(corr_ids) < 5:
                        corr_ids.append(corr_id)
                    if ok:
                        latencies.append(latency_ms)
                    else:
                        errors.append(f"{corr_id}: {reason}")
        finally:
            client.close()

    started = time.perf_counter()
    with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
        futures = [pool.submit(worker, idx) for idx in range(concurrency)]
        for fut in futures:
            fut.result()
    elapsed_s = max(time.perf_counter() - started, 1e-6)
    return {
        "requested": total,
        "succeeded": len(latencies),
        "failed": len(errors),
        "durationSeconds": round(elapsed_s, 4),
        "throughputRps": round(len(latencies) / elapsed_s, 2),
        "latency": summarize_latencies(latencies),
        "sampleCorrelationIds": corr_ids,
        "sampleErrors": errors[:10],
    }


def main() -> int:
    args = parse_args()
    warmup = run_batch(args.warmup, min(args.concurrency, max(args.warmup, 1)), args, "bench-inv-warm")
    if warmup["failed"] > 0:
        print(json.dumps({"status": "WARMUP_FAILED", "warmup": warmup}, indent=2))
        return 1

    sampler = DockerStatsSampler(args.docker_project, interval_s=0.5)
    sampler.start()
    measured = run_batch(args.requests, args.concurrency, args, "bench-inv")
    resources = sampler.stop()
    container_health = inspect_project_health(args.docker_project)

    report = {
        "benchmark": "inventory-read",
        "targetUrl": args.url,
        "sku": args.sku,
        "warehouse": args.warehouse,
        "concurrency": args.concurrency,
        "warmup": warmup,
        "measured": measured,
        "resources": resources,
        "containerHealth": container_health,
    }
    output_json = json.dumps(report, indent=2)
    if args.output:
        with open(args.output, "w", encoding="utf-8") as fh:
            fh.write(output_json + "\n")
    print(output_json)
    has_oom_or_restart = any(row["restarts"] > 0 or row["oomKilled"] for row in container_health)
    return 0 if measured["failed"] == 0 and not has_oom_or_restart else 1


if __name__ == "__main__":
    sys.exit(main())
