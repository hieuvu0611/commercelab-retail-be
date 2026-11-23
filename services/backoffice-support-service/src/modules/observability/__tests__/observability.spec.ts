import { ObservabilityController } from '../observability.controller';
import {
  defaultObservabilityRegistry,
  normalizePath,
  ObservabilityService,
  parseTraceparent,
  resolveTraceContext,
} from '../observability.service';
import { configureHttpFoundation } from '../../../common/http/http-foundation';

describe('Observability (W3C traceparent & Prometheus /metrics)', () => {
  it('parses incoming W3C traceparent and generates a child span preserving traceId', () => {
    const incoming = '00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01';
    const parsed = parseTraceparent(incoming);
    expect(parsed).toEqual({
      traceId: '4bf92f3577b34da6a3ce929d0e0e4736',
      spanId: '00f067aa0ba902b7',
      flags: '01',
    });

    const child = resolveTraceContext(incoming);
    expect(child.traceId).toBe('4bf92f3577b34da6a3ce929d0e0e4736');
    expect(child.spanId).not.toBe('00f067aa0ba902b7');
    expect(child.traceparent).toBe(`00-${child.traceId}-${child.spanId}-01`);

    const fresh = resolveTraceContext('invalid');
    expect(parseTraceparent(fresh.traceparent)).not.toBeNull();
  });

  it('normalizes dynamic path segments to keep metric cardinality bounded', () => {
    expect(normalizePath('/api/v1/backoffice/returns/RMA-102/approve')).toBe('/api/v1/backoffice/returns/:id/approve');
    expect(normalizePath('/api/v1/orders/O-001/returns')).toBe('/api/v1/orders/:id/returns');
  });

  it('exposes Prometheus text exposition metrics including open exception cases when DB responds', async () => {
    defaultObservabilityRegistry.recordHttpRequest('GET', '/api/v1/backoffice/returns/RMA-99', 200, 0.042);

    const mockDb = {
      query: jest.fn().mockResolvedValue({ rows: [{ count: '6' }] }),
    };
    const service = new ObservabilityService(mockDb as any);
    const controller = new ObservabilityController(service);

    const headers: Record<string, string> = {};
    let statusCode = 0;
    let payload = '';
    const mockRes = {
      setHeader: (k: string, v: string) => {
        headers[k] = v;
      },
      status: (code: number) => {
        statusCode = code;
        return mockRes;
      },
      send: (body: string) => {
        payload = body;
      },
    };

    await controller.metrics(mockRes as any);

    expect(statusCode).toBe(200);
    expect(headers['Content-Type']).toBe('text/plain; version=0.0.4; charset=utf-8');
    expect(payload).toContain('commercelab_service_up{service="backoffice-support-service"} 1');
    expect(payload).toContain('commercelab_http_requests_total{method="GET",path="/api/v1/backoffice/returns/:id",service="backoffice-support-service",status="200"}');
    expect(payload).toContain('commercelab_open_exception_cases 6');
  });

  it('serves /metrics gracefully when DB query fails', async () => {
    const failingDb = {
      query: jest.fn().mockRejectedValue(new Error('pg down')),
    };
    const service = new ObservabilityService(failingDb as any);
    const out = await service.renderMetrics();
    expect(out).toContain('commercelab_service_up{service="backoffice-support-service"} 1');
    expect(out).not.toContain('commercelab_open_exception_cases');
  });

  it('echoes W3C traceparent header via HTTP foundation middleware', () => {
    let installedMiddleware: any;
    const fakeApp = {
      use: (mw: any) => {
        installedMiddleware = mw;
      },
      useGlobalFilters: jest.fn(),
      useGlobalInterceptors: jest.fn(),
      useGlobalPipes: jest.fn(),
    };
    configureHttpFoundation(fakeApp as any);

    const incoming = '00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01';
    const resHeaders: Record<string, string> = {};
    const req: any = {
      method: 'GET',
      path: '/api/v1/returns',
      headers: { traceparent: incoming },
      header: (name: string) => req.headers[name.toLowerCase()],
    };
    const res: any = {
      statusCode: 200,
      setHeader: (k: string, v: string) => {
        resHeaders[k.toLowerCase()] = v;
      },
      on: jest.fn(),
    };
    const next = jest.fn();

    installedMiddleware(req, res, next);
    expect(next).toHaveBeenCalledTimes(1);
    const echoed = parseTraceparent(resHeaders['traceparent']);
    expect(echoed).not.toBeNull();
    expect(echoed?.traceId).toBe('4bf92f3577b34da6a3ce929d0e0e4736');
    expect(echoed?.spanId).not.toBe('00f067aa0ba902b7');
  });
});
