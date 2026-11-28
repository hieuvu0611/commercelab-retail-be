import { Injectable, Optional } from '@nestjs/common';
import { randomBytes } from 'node:crypto';
import { DatabaseService } from '../../database/database.service';

export const SERVICE_NAME = 'backoffice-support-service';

const HTTP_BUCKETS = [0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5];

const UUID_RE = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
const NUM_RE = /^[0-9]+$/;
const HEX_RE = /^[0-9a-fA-F]{16,}$/;
const PREFIX_ID_RE = /^(O|ord|PAY|SHP|RMA|RET|CASE|SKU|PO|NOTI|WH|user|addr|req|fam|rt)-.+$/;
const TRACEPARENT_RE = /^([0-9a-f]{2})-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})$/i;

export interface TraceContext {
  traceId: string;
  spanId: string;
  flags: string;
  traceparent: string;
}

export function parseTraceparent(header?: string): { traceId: string; spanId: string; flags: string } | null {
  if (!header) return null;
  const match = TRACEPARENT_RE.exec(header.trim());
  if (!match) return null;
  const version = match[1].toLowerCase();
  const traceId = match[2].toLowerCase();
  const spanId = match[3].toLowerCase();
  const flags = match[4].toLowerCase();
  if (version === 'ff' || /^0+$/.test(traceId) || /^0+$/.test(spanId)) {
    return null;
  }
  return { traceId, spanId, flags };
}

export function resolveTraceContext(header?: string): TraceContext {
  const parsed = parseTraceparent(header);
  const traceId = parsed ? parsed.traceId : randomBytes(16).toString('hex');
  const flags = parsed ? parsed.flags : '01';
  const spanId = randomBytes(8).toString('hex');
  return {
    traceId,
    spanId,
    flags,
    traceparent: `00-${traceId}-${spanId}-${flags}`,
  };
}

export function normalizePath(rawPath: string): string {
  const clean = rawPath.split('?')[0];
  if (!clean || clean === '/') return '/';
  return clean
    .split('/')
    .map((seg) => {
      if (!seg) return seg;
      if (seg.includes('@') || UUID_RE.test(seg) || NUM_RE.test(seg) || HEX_RE.test(seg) || PREFIX_ID_RE.test(seg)) {
        return ':id';
      }
      return seg;
    })
    .join('/');
}

function escapeLabel(value: string): string {
  return value.replace(/\\/g, '\\\\').replace(/\n/g, '\\n').replace(/"/g, '\\"');
}

interface HistogramEntry {
  buckets: number[];
  sum: number;
  count: number;
}

export class ObservabilityRegistry {
  private readonly requestCounts = new Map<string, { method: string; path: string; status: string; count: number }>();
  private readonly requestDurations = new Map<string, { method: string; path: string; hist: HistogramEntry }>();

  recordHttpRequest(method: string, rawPath: string, status: number, durationSeconds: number): void {
    const path = normalizePath(rawPath);
    const statusStr = String(status);
    const secs = Math.max(0, durationSeconds);

    const reqKey = `${method}|${path}|${statusStr}`;
    const existingReq = this.requestCounts.get(reqKey);
    if (existingReq) {
      existingReq.count += 1;
    } else {
      this.requestCounts.set(reqKey, { method, path, status: statusStr, count: 1 });
    }

    const durKey = `${method}|${path}`;
    let durEntry = this.requestDurations.get(durKey);
    if (!durEntry) {
      durEntry = {
        method,
        path,
        hist: {
          buckets: new Array(HTTP_BUCKETS.length).fill(0),
          sum: 0,
          count: 0,
        },
      };
      this.requestDurations.set(durKey, durEntry);
    }
    for (let i = 0; i < HTTP_BUCKETS.length; i += 1) {
      if (secs <= HTTP_BUCKETS[i]) {
        durEntry.hist.buckets[i] += 1;
      }
    }
    durEntry.hist.sum += secs;
    durEntry.hist.count += 1;
  }

  render(openExceptionCases?: number): string {
    const lines: string[] = [
      '# HELP commercelab_service_up Service liveness indicator (1 = up).',
      '# TYPE commercelab_service_up gauge',
      `commercelab_service_up{service="${SERVICE_NAME}"} 1`,
      '# HELP commercelab_http_requests_total Total HTTP requests handled.',
      '# TYPE commercelab_http_requests_total counter',
    ];

    const sortedReqs = Array.from(this.requestCounts.values()).sort((a, b) =>
      `${a.method}|${a.path}|${a.status}`.localeCompare(`${b.method}|${b.path}|${b.status}`),
    );
    for (const r of sortedReqs) {
      lines.push(
        `commercelab_http_requests_total{method="${escapeLabel(r.method)}",path="${escapeLabel(r.path)}",service="${SERVICE_NAME}",status="${escapeLabel(r.status)}"} ${r.count}`,
      );
    }

    lines.push(
      '# HELP commercelab_http_request_duration_seconds HTTP request latency in seconds.',
      '# TYPE commercelab_http_request_duration_seconds histogram',
    );
    const sortedDurs = Array.from(this.requestDurations.values()).sort((a, b) =>
      `${a.method}|${a.path}`.localeCompare(`${b.method}|${b.path}`),
    );
    for (const d of sortedDurs) {
      const base = `method="${escapeLabel(d.method)}",path="${escapeLabel(d.path)}",service="${SERVICE_NAME}"`;
      for (let i = 0; i < HTTP_BUCKETS.length; i += 1) {
        lines.push(`commercelab_http_request_duration_seconds_bucket{${base},le="${HTTP_BUCKETS[i]}"} ${d.hist.buckets[i]}`);
      }
      lines.push(`commercelab_http_request_duration_seconds_bucket{${base},le="+Inf"} ${d.hist.count}`);
      lines.push(`commercelab_http_request_duration_seconds_sum{${base}} ${d.hist.sum}`);
      lines.push(`commercelab_http_request_duration_seconds_count{${base}} ${d.hist.count}`);
    }

    if (typeof openExceptionCases === 'number' && Number.isFinite(openExceptionCases)) {
      lines.push(
        '# HELP commercelab_open_exception_cases Exception cases not yet resolved with evidence.',
        '# TYPE commercelab_open_exception_cases gauge',
        `commercelab_open_exception_cases ${openExceptionCases}`,
      );
    }

    return `${lines.join('\n')}\n`;
  }
}

export const defaultObservabilityRegistry = new ObservabilityRegistry();

@Injectable()
export class ObservabilityService {
  constructor(@Optional() private readonly db?: DatabaseService) {}

  async renderMetrics(): Promise<string> {
    let openCases: number | undefined;
    if (this.db) {
      try {
        const res = await this.db.query<{ count: string }>(
          "SELECT COUNT(*) AS count FROM support.exception_cases WHERE status <> 'RESOLVED_WITH_EVIDENCE'",
        );
        const parsed = Number(res.rows[0]?.count ?? 0);
        if (Number.isFinite(parsed)) {
          openCases = parsed;
        }
      } catch {
        openCases = undefined;
      }
    }
    return defaultObservabilityRegistry.render(openCases);
  }
}
