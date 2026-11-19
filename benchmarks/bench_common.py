"""Shared helpers for low-resource and staging HTTP load benchmarks."""

from __future__ import annotations

import http.client
import json
import math
import subprocess
import threading
import time
from typing import Any
from urllib.parse import urlparse


def percentile(sorted_values: list[float], pct: float) -> float:
    """Compute a linear-interpolated percentile in milliseconds from sorted samples."""
    if not sorted_values:
        return 0.0
    if len(sorted_values) == 1:
        return sorted_values[0]
    rank = (len(sorted_values) - 1) * (pct / 100.0)
    lower = math.floor(rank)
    upper = math.ceil(rank)
    if lower == upper:
        return sorted_values[lower]
    fraction = rank - lower
    return sorted_values[lower] + (sorted_values[upper] - sorted_values[lower]) * fraction


def summarize_latencies(latencies_ms: list[float]) -> dict[str, float]:
    """Return min, mean, p50, p95, p99, and max latency metrics in milliseconds."""
    if not latencies_ms:
        return {"minMs": 0.0, "meanMs": 0.0, "p50Ms": 0.0, "p95Ms": 0.0, "p99Ms": 0.0, "maxMs": 0.0}
    ordered = sorted(latencies_ms)
    return {
        "minMs": round(ordered[0], 3),
        "meanMs": round(sum(ordered) / len(ordered), 3),
        "p50Ms": round(percentile(ordered, 50), 3),
        "p95Ms": round(percentile(ordered, 95), 3),
        "p99Ms": round(percentile(ordered, 99), 3),
        "maxMs": round(ordered[-1], 3),
    }


class KeepAliveClient:
    """Persistent HTTP/1.1 keep-alive JSON client for a single worker thread."""

    def __init__(self, base_url: str, timeout_s: float = 10.0) -> None:
        parsed = urlparse(base_url)
        if parsed.scheme not in ("http", "https") or not parsed.hostname:
            raise ValueError(f"Unsupported base URL: {base_url}")
        self.scheme = parsed.scheme
        self.host = parsed.hostname
        self.port = parsed.port or (443 if self.scheme == "https" else 80)
        self.base_path = parsed.path.rstrip("/")
        self.timeout_s = timeout_s
        self._conn: http.client.HTTPConnection | None = None

    def _ensure_conn(self) -> http.client.HTTPConnection:
        if self._conn is None:
            cls = http.client.HTTPSConnection if self.scheme == "https" else http.client.HTTPConnection
            self._conn = cls(self.host, self.port, timeout=self.timeout_s)
        return self._conn

    def close(self) -> None:
        """Close any open connection."""
        if self._conn is not None:
            try:
                self._conn.close()
            finally:
                self._conn = None

    def request(
        self,
        method: str,
        path: str,
        *,
        headers: dict[str, str] | None = None,
        body: Any = None,
    ) -> tuple[int, dict[str, Any] | None, str, float]:
        """Execute one request, returning (status, parsed_json, raw_text, latency_ms)."""
        full_path = f"{self.base_path}{path}"
        req_headers = {"Connection": "keep-alive", "Accept": "application/json"}
        if headers:
            req_headers.update(headers)
        payload: bytes | None = None
        if body is not None:
            payload = json.dumps(body).encode("utf-8")
            req_headers.setdefault("Content-Type", "application/json")

        started = time.perf_counter()
        last_exc: Exception | None = None
        for attempt in range(2):
            conn = self._ensure_conn()
            try:
                conn.request(method, full_path, body=payload, headers=req_headers)
                resp = conn.getresponse()
                raw = resp.read().decode("utf-8", errors="replace")
                latency_ms = (time.perf_counter() - started) * 1000.0
                parsed: dict[str, Any] | None = None
                try:
                    loaded = json.loads(raw) if raw else None
                    if isinstance(loaded, dict):
                        parsed = loaded
                except json.JSONDecodeError:
                    parsed = None
                return resp.status, parsed, raw, latency_ms
            except (http.client.HTTPException, OSError) as exc:
                last_exc = exc
                self.close()
                if attempt == 1:
                    break
        latency_ms = (time.perf_counter() - started) * 1000.0
        return 0, None, str(last_exc or "connection error"), latency_ms


def _parse_percent(raw: str) -> float:
    cleaned = raw.strip().rstrip("%")
    try:
        return float(cleaned)
    except ValueError:
        return 0.0


def _parse_mem_mib(raw: str) -> float:
    used = raw.split("/", 1)[0].strip()
    multipliers = {
        "GiB": 1024.0,
        "GB": 1000.0 / 1.048576,
        "MiB": 1.0,
        "MB": 1.0 / 1.048576,
        "KiB": 1.0 / 1024.0,
        "kB": 1.0 / 1048.576,
        "B": 1.0 / (1024.0 * 1024.0),
    }
    for suffix, mult in multipliers.items():
        if used.endswith(suffix):
            try:
                return round(float(used[: -len(suffix)].strip()) * mult, 3)
            except ValueError:
                return 0.0
    return 0.0


def _project_containers(project: str) -> list[str]:
    if not project:
        return []
    proc = subprocess.run(
        ["docker", "ps", "-q", "--filter", f"label=com.docker.compose.project={project}"],
        capture_output=True,
        text=True,
        check=False,
    )
    return [line.strip() for line in proc.stdout.splitlines() if line.strip()]


def inspect_project_health(project: str) -> list[dict[str, Any]]:
    """Inspect restart counts and OOMKilled status for every container in a Compose project."""
    cids = _project_containers(project)
    if not cids:
        return []
    proc = subprocess.run(
        [
            "docker",
            "inspect",
            "--format",
            "{{.Name}}\t{{.State.Status}}\t{{.RestartCount}}\t{{.State.OOMKilled}}",
            *cids,
        ],
        capture_output=True,
        text=True,
        check=False,
    )
    rows: list[dict[str, Any]] = []
    for line in proc.stdout.splitlines():
        parts = line.split("\t")
        if len(parts) != 4:
            continue
        rows.append(
            {
                "container": parts[0].lstrip("/"),
                "status": parts[1],
                "restarts": int(parts[2] or 0),
                "oomKilled": parts[3].strip().lower() == "true",
            }
        )
    return sorted(rows, key=lambda row: row["container"])


class DockerStatsSampler:
    """Background sampler capturing `docker stats --no-stream` while load runs."""

    def __init__(self, project: str, interval_s: float = 1.0) -> None:
        self.project = project
        self.interval_s = interval_s
        self.samples: list[dict[str, Any]] = []
        self._stop = threading.Event()
        self._thread: threading.Thread | None = None

    def start(self) -> None:
        """Start periodic sampling if a Docker project name is configured."""
        if not self.project:
            return
        self._thread = threading.Thread(target=self._run, daemon=True)
        self._thread.start()

    def stop(self) -> dict[str, Any]:
        """Stop sampling and return per-container peak and average CPU/memory summaries."""
        if self._thread is not None:
            self._stop.set()
            self._thread.join(timeout=5.0)
        by_container: dict[str, list[dict[str, Any]]] = {}
        for snap in self.samples:
            for item in snap["containers"]:
                by_container.setdefault(item["container"], []).append(item)

        summary: dict[str, Any] = {}
        for name, items in sorted(by_container.items()):
            cpus = [x["cpuPct"] for x in items]
            mems = [x["memMib"] for x in items]
            summary[name] = {
                "samples": len(items),
                "avgCpuPct": round(sum(cpus) / len(cpus), 2),
                "peakCpuPct": round(max(cpus), 2),
                "avgMemMib": round(sum(mems) / len(mems), 3),
                "peakMemMib": round(max(mems), 3),
            }
        return {"sampleCount": len(self.samples), "containers": summary, "rawSamples": self.samples}

    def _run(self) -> None:
        while not self._stop.is_set():
            cids = _project_containers(self.project)
            if cids:
                proc = subprocess.run(
                    ["docker", "stats", "--no-stream", "--format", "{{json .}}", *cids],
                    capture_output=True,
                    text=True,
                    check=False,
                )
                containers: list[dict[str, Any]] = []
                for line in proc.stdout.splitlines():
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        parsed = json.loads(line)
                    except json.JSONDecodeError:
                        continue
                    containers.append(
                        {
                            "container": str(parsed.get("Name", "")).lstrip("/"),
                            "cpuPct": _parse_percent(str(parsed.get("CPUPerc", "0%"))),
                            "memUsage": str(parsed.get("MemUsage", "")),
                            "memMib": _parse_mem_mib(str(parsed.get("MemUsage", ""))),
                            "memPct": _parse_percent(str(parsed.get("MemPerc", "0%"))),
                        }
                    )
                if containers:
                    self.samples.append({"ts": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "containers": containers})
            self._stop.wait(self.interval_s)
