"""Controlled arrival-rate capacity benchmark for CommerceLab Retail (Explain the Number / Docs 02 §9)."""

from __future__ import annotations

import argparse
import concurrent.futures
import json
import os
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
from typing import Any

from bench_common import KeepAliveClient, summarize_latencies


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(description="Run arrival-rate step-up capacity benchmark.")
    p.add_argument("--url", default="http://127.0.0.1:38080", help="API gateway URL")
    p.add_argument("--levels", default="100,300,500", help="Comma-separated target RPS levels")
    p.add_argument("--repeats", type=int, default=3, help="Repeated runs per RPS level")
    p.add_argument("--duration-seconds", type=int, default=15, help="Duration per run")
    p.add_argument("--max-workers", type=int, default=64, help="Thread pool size")
    p.add_argument("--k8s-context", default="kind-commercelab-k8s-lab", help="Optional kubectl context for pod/DB checks")
    p.add_argument("--output", required=True, help="Output JSON report path")
    return p.parse_args()


def git_sha() -> str:
    return subprocess.check_output(["git", "-C", os.path.dirname(os.path.abspath(__file__)), "rev-parse", "--short", "HEAD"], text=True).strip()


def login_token(url: str, email: str) -> str:
    c = KeepAliveClient(url)
    try:
        code, parsed, raw, _ = c.request("POST", "/api/v1/auth/login", body={"email": email, "password": "Dev-Passw0rd-2026"})
        if code != 200 or not parsed:
            raise RuntimeError(f"Login failed ({email}): {code} {raw}")
        return str(parsed["data"]["accessToken"])
    finally:
        c.close()


def sample_k8s_resources(ctx: str) -> dict[str, Any]:
    if not ctx:
        return {}
    p = subprocess.run(["kubectl", "--context", ctx, "-n", "commercelab-retail", "top", "pods", "--no-headers"], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    if p.returncode != 0:
        return {"topAvailable": False, "note": p.stderr.strip()}
    rows = []
    for line in p.stdout.strip().splitlines():
        parts = line.split()
        if len(parts) >= 3:
            rows.append({"pod": parts[0], "cpu": parts[1], "memory": parts[2]})
    return {"topAvailable": True, "pods": rows}


def audit_k8s_postgres(ctx: str) -> dict[str, Any]:
    if not ctx:
        return {}
    pod = subprocess.check_output(["kubectl", "--context", ctx, "-n", "commercelab-infra", "get", "pods", "-l", "app=postgres", "-o", "jsonpath={.items[0].metadata.name}"], text=True).strip()
    def q(sql: str) -> str:
        return subprocess.check_output(["kubectl", "--context", ctx, "-n", "commercelab-infra", "exec", pod, "--", "psql", "-U", "postgres", "-d", "commerce_lab", "-Atc", sql], text=True).strip()
    neg = int(q("SELECT COUNT(*) FROM inventory.stock_items WHERE on_hand < 0 OR unsellable < 0 OR reserved < 0 OR committed < 0 OR (on_hand - unsellable - reserved - committed) < 0;"))
    dc = q("SELECT COALESCE(SUM(CASE WHEN direction='DEBIT' THEN amount_vnd ELSE 0 END),0), COALESCE(SUM(CASE WHEN direction='CREDIT' THEN amount_vnd ELSE 0 END),0) FROM ledger.ledger_postings;")
    d_str, c_str = dc.split("|")
    return {
        "negativeStockRows": neg,
        "totalDebitVnd": int(d_str),
        "totalCreditVnd": int(c_str),
        "ledgerBalanced": int(d_str) == int(c_str),
    }


def run_single(url: str, target_rps: int, duration: int, max_workers: int, cust_tok: str, pos_tok: str, run_tag: str) -> dict[str, Any]:
    total_target = target_rps * duration
    lock = threading.Lock()
    latencies: list[float] = []
    counts = {"ok": 0, "businessRejected": 0, "infraError": 0, "dropped": 0}
    tls = threading.local()
    clients: list[KeepAliveClient] = []

    def get_client() -> KeepAliveClient:
        cli = getattr(tls, "client", None)
        if cli is None:
            cli = KeepAliveClient(url, timeout_s=5.0)
            tls.client = cli
            with lock:
                clients.append(cli)
        return cli

    def execute_slot(idx: int) -> None:
        cli = get_client()
        slot = idx % 10
        corr = f"{run_tag}-{idx}"
        if slot < 7:
            path = "/api/v1/products?q=backpack&limit=10" if idx % 2 == 0 else "/api/v1/inventory/availability?skuIds=SKU-BOTTLE-BLUE-500ML&warehouse=ALL"
            code, _, _, lat = cli.request("GET", path, headers={"X-Correlation-ID": corr})
        elif slot < 9:
            path = "/api/v1/promotions/active" if idx % 2 == 0 else "/api/v1/carts/me"
            code, _, _, lat = cli.request("GET", path, headers={"Authorization": f"Bearer {cust_tok}", "X-Correlation-ID": corr})
        else:
            code, _, _, lat = cli.request(
                "POST",
                "/api/v1/admin/pos/sales",
                headers={
                    "Authorization": f"Bearer {pos_tok}",
                    "Idempotency-Key": f"cap-{run_tag}-{idx}",
                    "X-Correlation-ID": corr,
                },
                body={"warehouseId": "WH-HCM-01", "paymentMethod": "POS_CASH", "cashReceivedVnd": 100000, "items": [{"skuId": "SKU-BOTTLE-BLUE-500ML", "quantity": 1}]},
            )
        with lock:
            if code in (200, 201):
                counts["ok"] += 1
                latencies.append(lat)
            elif 400 <= code < 500:
                counts["businessRejected"] += 1
                latencies.append(lat)
            else:
                counts["infraError"] += 1

    start = time.perf_counter()
    interval = 1.0 / target_rps
    in_flight_limit = max_workers * 4
    pending: set[concurrent.futures.Future[Any]] = set()
    with concurrent.futures.ThreadPoolExecutor(max_workers=max_workers) as pool:
        for i in range(total_target):
            target_t = start + i * interval
            now = time.perf_counter()
            if target_t > now:
                time.sleep(target_t - now)
            done = {f for f in pending if f.done()}
            pending -= done
            if len(pending) >= in_flight_limit:
                counts["dropped"] += 1
                continue
            pending.add(pool.submit(execute_slot, i))
        for f in concurrent.futures.as_completed(pending):
            try:
                f.result()
            except Exception:
                with lock:
                    counts["infraError"] += 1

    elapsed = time.perf_counter() - start
    for c in clients:
        c.close()

    attempted = counts["ok"] + counts["businessRejected"] + counts["infraError"]
    summary = summarize_latencies(latencies)
    return {
        "targetRps": target_rps,
        "durationSec": round(elapsed, 3),
        "scheduledRequests": total_target,
        "attemptedRequests": attempted,
        "droppedRequests": counts["dropped"],
        "achievedRps": round(attempted / elapsed, 2),
        "ok": counts["ok"],
        "businessRejected": counts["businessRejected"],
        "infraError": counts["infraError"],
        "infraErrorPct": round((counts["infraError"] / attempted * 100.0) if attempted else 0.0, 3),
        "latencyMs": summary,
    }


def main() -> None:
    args = parse_args()
    levels = [int(x.strip()) for x in args.levels.split(",") if x.strip()]
    cust_tok = login_token(args.url, "lan@example.com")
    pos_tok = login_token(args.url, "wh.hcm@commercelab.vn")

    # Warmup 3s at 50 RPS
    _ = run_single(args.url, 50, 3, args.max_workers, cust_tok, pos_tok, f"warmup-{int(time.time())}")

    report: dict[str, Any] = {
        "timestampUtc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "commitSha": git_sha(),
        "url": args.url,
        "workloadMix": "70% catalog/inventory reads, 20% promo/cart reads, 10% POS checkout",
        "repeatsPerLevel": args.repeats,
        "durationSecondsPerRun": args.duration_seconds,
        "levels": [],
    }

    for lvl in levels:
        runs = []
        for r in range(args.repeats):
            tag = f"cap-{lvl}-r{r}-{int(time.time())}"
            res = run_single(args.url, lvl, args.duration_seconds, args.max_workers, cust_tok, pos_tok, tag)
            runs.append(res)
            time.sleep(2)
        achieved_vals = [x["achievedRps"] for x in runs]
        p95_vals = [x["latencyMs"]["p95Ms"] for x in runs]
        report["levels"].append({
            "targetRps": lvl,
            "runs": runs,
            "achievedRpsMinMax": [min(achieved_vals), max(achieved_vals)],
            "p95MsMinMax": [min(p95_vals), max(p95_vals)],
        })

    report["k8sResourceSample"] = sample_k8s_resources(args.k8s_context)
    report["invariantAudit"] = audit_k8s_postgres(args.k8s_context)

    os.makedirs(os.path.dirname(args.output), exist_ok=True)
    with open(args.output, "w") as f:
        json.dump(report, f, indent=2)
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
